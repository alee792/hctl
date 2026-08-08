package discordadapter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"hctl/channeladapter"

	"github.com/bwmarrin/discordgo"
)

const componentPrefix = "h1"

func (runtime *Runtime) renderInteraction(hostFrameID string, request channeladapter.InteractionRequest) {
	handle := opaqueHandle("interaction", request.InteractionID)
	pending := pendingInteraction{hostFrameID: hostFrameID, request: request, handle: handle}
	runtime.mu.Lock()
	runtime.interactions[request.InteractionID] = pending
	runtime.handles[handle] = request.InteractionID
	runtime.mu.Unlock()
	var message *discordgo.MessageSend
	var err error
	if runtime.supports(channeladapter.FeatureInteractiveComponents) {
		message, err = interactionMessage(pending)
	} else {
		message, err = fallbackMessage(pending)
	}
	if err == nil && len(message.Components) == 0 && !runtime.supports(channeladapter.FeatureTextFallback) {
		err = errors.New("text fallback was not negotiated")
	}
	if err != nil {
		_ = runtime.writer.send(channeladapter.Diagnostic{Class: channeladapter.DiagnosticConfiguration, Severity: channeladapter.SeverityWarning, Code: "interaction_fallback_failed", Message: "Discord could not render the interactive request."}, hostFrameID)
		return
	}
	if _, err := runtime.client.ChannelMessageSendComplex(request.Route.Handle, message); err != nil {
		_ = runtime.writer.send(channeladapter.Diagnostic{Class: channeladapter.DiagnosticConnection, Severity: channeladapter.SeverityWarning, Code: "interaction_delivery_uncertain", Message: "Discord interaction delivery may have failed."}, hostFrameID)
	}
}

func interactionMessage(pending pendingInteraction) (*discordgo.MessageSend, error) {
	request := pending.request.Request
	if len([]rune(request.Prompt)) > 2000 {
		return fallbackMessage(pending)
	}
	message := &discordgo.MessageSend{
		Content: request.Prompt, AllowedMentions: disabledMentions(),
		Reference: &discordgo.MessageReference{MessageID: pending.request.ReplyTo.Handle, ChannelID: pending.request.Route.Handle, FailIfNotExists: boolPointer(false)},
	}
	cancelButton := func() discordgo.MessageComponent {
		return discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: customID(pending.handle, "x")}
	}
	switch request.Kind {
	case channeladapter.InteractionConfirm:
		components := []discordgo.MessageComponent{
			discordgo.Button{Label: "Yes", Style: discordgo.SuccessButton, CustomID: customID(pending.handle, "y")},
			discordgo.Button{Label: "No", Style: discordgo.DangerButton, CustomID: customID(pending.handle, "n")},
		}
		if request.Policy.Cancellation == channeladapter.CancellationAllowed {
			components = append(components, cancelButton())
		}
		message.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: components}}
	case channeladapter.InteractionChooseOne, channeladapter.InteractionChooseMany:
		if request.Field == nil || request.Field.AllowFreeform {
			return fallbackMessage(pending)
		}
		options := make([]discordgo.SelectMenuOption, len(request.Field.Options))
		for index, option := range request.Field.Options {
			options[index] = discordgo.SelectMenuOption{Label: option.Label, Description: option.Description, Value: "v" + strconv.Itoa(index)}
		}
		minimum, maximum := request.Field.MinSelections, request.Field.MaxSelections
		if request.Kind == channeladapter.InteractionChooseOne {
			minimum, maximum = 1, 1
		}
		message.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{CustomID: customID(pending.handle, "s"), Placeholder: request.Field.Label, MinValues: &minimum, MaxValues: maximum, Options: options}}}}
		if request.Policy.Cancellation == channeladapter.CancellationAllowed {
			message.Components = append(message.Components, discordgo.ActionsRow{Components: []discordgo.MessageComponent{cancelButton()}})
		}
	case channeladapter.InteractionText:
		if request.Field == nil || len([]rune(request.Field.Label)) > 45 {
			return fallbackMessage(pending)
		}
		components := []discordgo.MessageComponent{discordgo.Button{Label: "Answer", Style: discordgo.PrimaryButton, CustomID: customID(pending.handle, "a")}}
		if request.Policy.Cancellation == channeladapter.CancellationAllowed {
			components = append(components, cancelButton())
		}
		message.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: components}}
	case channeladapter.InteractionForm:
		if len(request.Fields) > 5 {
			return fallbackMessage(pending)
		}
		for _, field := range request.Fields {
			if field.Kind != channeladapter.InteractionText || len([]rune(field.Label)) > 45 {
				return fallbackMessage(pending)
			}
		}
		components := []discordgo.MessageComponent{discordgo.Button{Label: "Answer", Style: discordgo.PrimaryButton, CustomID: customID(pending.handle, "a")}}
		if request.Policy.Cancellation == channeladapter.CancellationAllowed {
			components = append(components, cancelButton())
		}
		message.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: components}}
	default:
		return fallbackMessage(pending)
	}
	return message, nil
}

func fallbackMessage(pending pendingInteraction) (*discordgo.MessageSend, error) {
	request := pending.request.Request
	if request.FallbackText == "" {
		return nil, errors.New("interactive request has no supported Discord rendering or fallback")
	}
	instructions := textInstructions(request)
	content := request.Prompt + "\n\n" + request.FallbackText + "\n\n" + instructions + "\n\n[hctl request " + pending.handle + "]"
	if len([]rune(content)) > 2000 {
		return nil, errors.New("interactive fallback exceeds Discord message limit")
	}
	return &discordgo.MessageSend{Content: content, AllowedMentions: disabledMentions(), Reference: &discordgo.MessageReference{MessageID: pending.request.ReplyTo.Handle, ChannelID: pending.request.Route.Handle, FailIfNotExists: boolPointer(false)}}, nil
}

func customID(handle, action string) string { return componentPrefix + "." + handle + "." + action }

func parseCustomID(value string) (string, string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != componentPrefix || len(parts[1]) != 32 || len(parts[2]) == 0 || len(parts[2]) > 4 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func (runtime *Runtime) handleInteraction(incoming *discordgo.InteractionCreate) {
	if incoming == nil || incoming.Interaction == nil || !runtime.authorizedInteraction(incoming) {
		return
	}
	if incoming.Type == discordgo.InteractionApplicationCommand {
		data := incoming.ApplicationCommandData()
		var action channeladapter.ControlAction
		switch data.Name {
		case "status":
			action = channeladapter.ControlStatus
		case "new":
			action = channeladapter.ControlReset
		default:
			return
		}
		_, _ = runtime.writer.sendEventRegistered("control:"+incoming.ID, channeladapter.ControlRequest{SourceID: incoming.ID, Route: channeladapter.Route{Handle: incoming.ChannelID}, Message: channeladapter.MessageRef{Handle: incoming.ID}, Action: action}, "", func(frameID string, add bool) {
			runtime.mu.Lock()
			if add {
				runtime.controls[frameID] = pendingControl{interaction: incoming.Interaction, action: action}
			} else {
				delete(runtime.controls, frameID)
			}
			runtime.mu.Unlock()
		})
		return
	}
	if incoming.Type != discordgo.InteractionMessageComponent && incoming.Type != discordgo.InteractionModalSubmit {
		return
	}
	custom := ""
	if incoming.Type == discordgo.InteractionMessageComponent {
		custom = incoming.MessageComponentData().CustomID
	} else {
		custom = incoming.ModalSubmitData().CustomID
	}
	handle, action, ok := parseCustomID(custom)
	if !ok {
		return
	}
	runtime.mu.Lock()
	interactionID := runtime.handles[handle]
	pending, ok := runtime.interactions[interactionID]
	runtime.mu.Unlock()
	if !ok || pending.request.Route.Handle != incoming.ChannelID {
		return
	}
	if action == "a" {
		modal, err := interactionModal(pending)
		if err == nil {
			_ = runtime.client.InteractionRespond(incoming.Interaction, modal)
		}
		return
	}
	answer, err := nativeAnswer(incoming, pending, action)
	if err != nil {
		return
	}
	_ = runtime.finishInteraction(pending, answer, pendingCallback{interaction: incoming.Interaction, cancelled: answer.Action == channeladapter.AnswerCancel})
}

func (runtime *Runtime) authorizedInteraction(incoming *discordgo.InteractionCreate) bool {
	if incoming.AppID != "" && incoming.AppID != runtime.profile.ApplicationID {
		return false
	}
	userID := ""
	if incoming.Member != nil && incoming.Member.User != nil {
		userID = incoming.Member.User.ID
	} else if incoming.User != nil {
		userID = incoming.User.ID
	}
	if userID != runtime.profile.AllowedUserID {
		return false
	}
	return incoming.GuildID == "" || incoming.GuildID == runtime.profile.AllowedGuildID && incoming.ChannelID == runtime.profile.AllowedChannelID
}

func interactionModal(pending pendingInteraction) (*discordgo.InteractionResponse, error) {
	request := pending.request.Request
	fields := request.Fields
	if request.Kind == channeladapter.InteractionText && request.Field != nil {
		fields = []channeladapter.Field{*request.Field}
	}
	if len(fields) == 0 || len(fields) > 5 {
		return nil, errors.New("Discord modal field count is unsupported")
	}
	rows := make([]discordgo.MessageComponent, len(fields))
	for index, field := range fields {
		rows[index] = discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.TextInput{CustomID: "f" + strconv.Itoa(index), Label: field.Label, Style: discordgo.TextInputParagraph, Required: field.Required, MinLength: field.MinLength, MaxLength: field.MaxLength}}}
	}
	return &discordgo.InteractionResponse{Type: discordgo.InteractionResponseModal, Data: &discordgo.InteractionResponseData{CustomID: customID(pending.handle, "m"), Title: "hctl request", Components: rows}}, nil
}

func nativeAnswer(incoming *discordgo.InteractionCreate, pending pendingInteraction, action string) (channeladapter.SemanticInteractionAnswer, error) {
	request := pending.request.Request
	if action == "x" && request.Policy.Cancellation == channeladapter.CancellationAllowed {
		return channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerCancel}, nil
	}
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerSubmit}
	switch request.Kind {
	case channeladapter.InteractionConfirm:
		value := action == "y"
		if action != "y" && action != "n" {
			return answer, errors.New("invalid confirmation action")
		}
		answer.Fields = []channeladapter.FieldAnswer{{FieldID: request.Field.ID, Confirmed: &value}}
	case channeladapter.InteractionChooseOne, channeladapter.InteractionChooseMany:
		data := incoming.MessageComponentData()
		ids := make([]string, 0, len(data.Values))
		for _, value := range data.Values {
			if len(value) < 2 || value[0] != 'v' {
				return answer, errors.New("invalid choice slot")
			}
			index, err := strconv.Atoi(value[1:])
			if err != nil || index < 0 || index >= len(request.Field.Options) {
				return answer, errors.New("invalid choice slot")
			}
			ids = append(ids, request.Field.Options[index].ID)
		}
		answer.Fields = []channeladapter.FieldAnswer{{FieldID: request.Field.ID, OptionIDs: ids}}
	case channeladapter.InteractionText, channeladapter.InteractionForm:
		data := incoming.ModalSubmitData()
		fields := request.Fields
		if request.Kind == channeladapter.InteractionText {
			fields = []channeladapter.Field{*request.Field}
		}
		values := modalValues(data.Components, len(fields))
		if len(values) != len(fields) {
			return answer, errors.New("invalid modal values")
		}
		for index, field := range fields {
			value := values[index]
			answer.Fields = append(answer.Fields, channeladapter.FieldAnswer{FieldID: field.ID, Text: &value})
		}
	default:
		return answer, errors.New("unsupported native interaction")
	}
	return answer, nil
}

func modalValues(components []discordgo.MessageComponent, count int) []string {
	values := make([]string, count)
	seen := make([]bool, count)
	for _, component := range components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			if value, valueOK := component.(discordgo.ActionsRow); valueOK {
				row = &value
			} else {
				continue
			}
		}
		for _, child := range row.Components {
			input, ok := child.(*discordgo.TextInput)
			if !ok {
				if value, valueOK := child.(discordgo.TextInput); valueOK {
					input = &value
				} else {
					continue
				}
			}
			if len(input.CustomID) < 2 || input.CustomID[0] != 'f' {
				continue
			}
			index, err := strconv.Atoi(input.CustomID[1:])
			if err == nil && index >= 0 && index < count && !seen[index] {
				values[index], seen[index] = input.Value, true
			}
		}
	}
	for _, ok := range seen {
		if !ok {
			return nil
		}
	}
	return values
}

func (runtime *Runtime) finishInteraction(pending pendingInteraction, answer channeladapter.SemanticInteractionAnswer, callback pendingCallback) error {
	runtime.mu.Lock()
	delete(runtime.interactions, pending.request.InteractionID)
	delete(runtime.handles, pending.handle)
	runtime.mu.Unlock()
	eventID, err := runtime.writer.sendEventRegistered("interaction:"+pending.request.InteractionID, channeladapter.InteractionResult{InteractionID: pending.request.InteractionID, Answer: answer}, pending.hostFrameID, func(id string, add bool) {
		if callback == (pendingCallback{}) {
			return
		}
		runtime.mu.Lock()
		if add {
			runtime.callbacks[id] = callback
		} else {
			delete(runtime.callbacks, id)
		}
		runtime.mu.Unlock()
	})
	if err != nil {
		return err
	}
	if callback != (pendingCallback{}) {
		go runtime.expireCallback(eventID)
	}
	return nil
}

func (runtime *Runtime) cancelInteraction(hostFrameID string, cancel channeladapter.InteractionCancel) {
	runtime.mu.Lock()
	pending, ok := runtime.interactions[cancel.InteractionID]
	runtime.mu.Unlock()
	if !ok {
		return
	}
	pending.hostFrameID = hostFrameID
	_ = runtime.finishInteraction(pending, channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerCancel}, pendingCallback{})
}

func (runtime *Runtime) handleFallback(incoming *discordgo.MessageCreate) bool {
	if incoming == nil || incoming.ReferencedMessage == nil {
		return false
	}
	content := incoming.ReferencedMessage.Content
	prefix := "[hctl request "
	index := strings.LastIndex(content, prefix)
	if index < 0 {
		return false
	}
	end := strings.Index(content[index:], "]")
	if end < 0 {
		return false
	}
	handle := content[index+len(prefix) : index+end]
	runtime.mu.Lock()
	interactionID := runtime.handles[handle]
	pending, ok := runtime.interactions[interactionID]
	runtime.mu.Unlock()
	if !ok || pending.request.Route.Handle != incoming.ChannelID {
		return false
	}
	answer, err := fallbackAnswer(pending.request.Request, incoming.Content)
	if err != nil {
		_, _ = runtime.client.ChannelMessageSendComplex(incoming.ChannelID, &discordgo.MessageSend{Content: "That answer does not match the requested format. Reply again using the shown format.", AllowedMentions: disabledMentions(), Reference: &discordgo.MessageReference{MessageID: incoming.ID, ChannelID: incoming.ChannelID, FailIfNotExists: boolPointer(false)}})
		return true
	}
	_ = runtime.finishInteraction(pending, answer, pendingCallback{route: incoming.ChannelID, message: incoming.ID, cancelled: answer.Action == channeladapter.AnswerCancel})
	return true
}

func (runtime *Runtime) expireCallback(eventID string) {
	<-runtime.after(channeladapter.CommandTimeout)
	runtime.mu.Lock()
	_, pending := runtime.callbacks[eventID]
	if pending {
		delete(runtime.callbacks, eventID)
	}
	runtime.mu.Unlock()
	if pending {
		select {
		case runtime.fatal <- errors.New("Discord interaction acknowledgement timed out"):
		default:
		}
	}
}

func (runtime *Runtime) acknowledgeInteraction(eventID, disposition string) {
	runtime.mu.Lock()
	callback, ok := runtime.callbacks[eventID]
	delete(runtime.callbacks, eventID)
	runtime.mu.Unlock()
	if !ok {
		return
	}
	message := "Answer received."
	if callback.cancelled {
		message = "Request cancelled."
	}
	if disposition == "rejected" {
		message = "That answer could not be accepted. Please try again."
	}
	if callback.interaction != nil {
		_ = runtime.client.InteractionRespond(callback.interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseUpdateMessage, Data: &discordgo.InteractionResponseData{Content: message, AllowedMentions: disabledMentions()}})
		return
	}
	_, _ = runtime.client.ChannelMessageSendComplex(callback.route, &discordgo.MessageSend{Content: message, AllowedMentions: disabledMentions(), Reference: &discordgo.MessageReference{MessageID: callback.message, ChannelID: callback.route, FailIfNotExists: boolPointer(false)}})
}

func fallbackAnswer(request channeladapter.SemanticInteractionRequest, content string) (channeladapter.SemanticInteractionAnswer, error) {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerSubmit}
	if content == "cancel" {
		if request.Policy.Cancellation != channeladapter.CancellationAllowed {
			return answer, errors.New("cancellation is not allowed")
		}
		answer.Action = channeladapter.AnswerCancel
		return answer, nil
	}
	if request.Kind != channeladapter.InteractionForm {
		field, err := parseFallbackField(*request.Field, content)
		if err != nil {
			return answer, err
		}
		answer.Fields = []channeladapter.FieldAnswer{field}
		return normalizeFallbackAnswer(request, answer)
	}
	byID := make(map[string]channeladapter.Field, len(request.Fields))
	for _, field := range request.Fields {
		byID[field.ID] = field
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" || seen[key] {
			return answer, errors.New("form reply must contain unique keyed lines")
		}
		field, found := byID[key]
		if !found {
			return answer, errors.New("form reply contains an unknown field")
		}
		parsed, err := parseFallbackField(field, strings.TrimSpace(value))
		if err != nil {
			return answer, err
		}
		seen[key] = true
		answer.Fields = append(answer.Fields, parsed)
	}
	return normalizeFallbackAnswer(request, answer)
}

func parseFallbackField(field channeladapter.Field, value string) (channeladapter.FieldAnswer, error) {
	answer := channeladapter.FieldAnswer{FieldID: field.ID}
	switch field.Kind {
	case channeladapter.InteractionConfirm:
		switch value {
		case "yes":
			confirmed := true
			answer.Confirmed = &confirmed
		case "no":
			confirmed := false
			answer.Confirmed = &confirmed
		default:
			return answer, errors.New("confirmation reply must be exactly yes or no")
		}
	case channeladapter.InteractionChooseOne, channeladapter.InteractionChooseMany:
		choiceValue, freeform := value, ""
		freeformPresent := false
		if field.AllowFreeform {
			if strings.HasPrefix(choiceValue, "other=") {
				freeformPresent, freeform, choiceValue = true, strings.TrimPrefix(choiceValue, "other="), ""
			} else if before, after, found := strings.Cut(choiceValue, ";other="); found {
				if field.Kind != channeladapter.InteractionChooseMany {
					return answer, errors.New("choose-one reply has invalid freeform syntax")
				}
				freeformPresent, choiceValue, freeform = true, before, after
			}
		}
		var ordinals []int
		var err error
		if choiceValue != "" {
			ordinals, err = parseOrdinals(choiceValue, field.Kind == channeladapter.InteractionChooseMany, len(field.Options))
		} else if !freeformPresent {
			err = errors.New("choice reply is empty")
		}
		if err != nil {
			return answer, err
		}
		for _, ordinal := range ordinals {
			answer.OptionIDs = append(answer.OptionIDs, field.Options[ordinal-1].ID)
		}
		if freeformPresent {
			answer.Freeform = &freeform
		}
	case channeladapter.InteractionText:
		answer.Text = &value
	case channeladapter.InteractionDateTime:
		answer.DateTime = &value
	default:
		return answer, errors.New("unsupported fallback shape")
	}
	return answer, nil
}

func parseOrdinals(value string, many bool, optionCount int) ([]int, error) {
	value = strings.TrimSpace(value)
	parts := []string{value}
	if many {
		parts = strings.Split(value, ",")
	} else if strings.Contains(value, ",") {
		return nil, errors.New("choose-one reply must contain one option number")
	}
	seen := map[int]bool{}
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		ordinal, err := strconv.Atoi(part)
		if err != nil || strconv.Itoa(ordinal) != part || ordinal < 1 || ordinal > optionCount || seen[ordinal] {
			return nil, errors.New("choice reply contains an invalid option number")
		}
		seen[ordinal] = true
		result = append(result, ordinal)
	}
	return result, nil
}

func normalizeFallbackAnswer(request channeladapter.SemanticInteractionRequest, answer channeladapter.SemanticInteractionAnswer) (channeladapter.SemanticInteractionAnswer, error) {
	fields := request.Fields
	if request.Field != nil {
		fields = []channeladapter.Field{*request.Field}
	}
	byID := map[string]channeladapter.FieldAnswer{}
	for _, value := range answer.Fields {
		if _, exists := byID[value.FieldID]; exists {
			return answer, errors.New("interactive answer repeats a field")
		}
		byID[value.FieldID] = value
	}
	normalized := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: answer.Action}
	for _, field := range fields {
		value, present := byID[field.ID]
		if !present {
			if field.Required {
				return answer, errors.New("interactive answer omits a required field")
			}
			continue
		}
		delete(byID, field.ID)
		switch field.Kind {
		case channeladapter.InteractionChooseOne, channeladapter.InteractionChooseMany:
			count := len(value.OptionIDs)
			if value.Freeform != nil {
				freeform := strings.TrimSpace(strings.ReplaceAll(*value.Freeform, "\r", ""))
				if !field.AllowFreeform || len([]rune(freeform)) < field.MinLength || len([]rune(freeform)) > field.MaxLength {
					return answer, errors.New("choice freeform answer is outside its bounds")
				}
				value.Freeform = &freeform
				count++
			}
			if count < field.MinSelections || count > field.MaxSelections {
				return answer, errors.New("choice answer violates selection cardinality")
			}
		case channeladapter.InteractionText:
			if value.Text == nil || len([]rune(*value.Text)) < field.MinLength || len([]rune(*value.Text)) > field.MaxLength {
				return answer, errors.New("text answer is outside its bounds")
			}
		case channeladapter.InteractionDateTime:
			if value.DateTime == nil {
				return answer, errors.New("date-time answer is missing")
			}
			normalizedValue, err := normalizeFallbackDateTime(*value.DateTime, field.DateTimeRepresentation)
			if err != nil {
				return answer, err
			}
			value.DateTime = &normalizedValue
		}
		normalized.Fields = append(normalized.Fields, value)
	}
	if len(byID) != 0 {
		return answer, errors.New("interactive answer contains an unknown field")
	}
	return normalized, nil
}

func normalizeFallbackDateTime(value string, representation channeladapter.DateTimeRepresentation) (string, error) {
	var parsed time.Time
	var err error
	switch representation {
	case channeladapter.DateOnly:
		parsed, err = time.Parse("2006-01-02", value)
		if err == nil {
			return parsed.Format("2006-01-02"), nil
		}
	case channeladapter.TimeOnly:
		parsed, err = time.Parse("15:04", value)
		if err == nil {
			return parsed.Format("15:04"), nil
		}
	case channeladapter.DateTime:
		parsed, err = time.Parse(time.RFC3339, value)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", errors.New("date-time answer does not match its representation")
}

func textInstructions(request channeladapter.SemanticInteractionRequest) string {
	var lines []string
	if request.Kind == channeladapter.InteractionForm {
		lines = append(lines, "Reply with one keyed line per field:")
		for _, field := range request.Fields {
			lines = append(lines, fmt.Sprintf("- %s: %s", field.ID, fieldTextGrammar(field)))
		}
	} else {
		lines = append(lines, "Reply with "+fieldTextGrammar(*request.Field)+".")
	}
	if request.Policy.Cancellation == channeladapter.CancellationAllowed {
		lines = append(lines, "Reply with exactly `cancel` to cancel.")
	}
	return strings.Join(lines, "\n")
}

func fieldTextGrammar(field channeladapter.Field) string {
	switch field.Kind {
	case channeladapter.InteractionConfirm:
		return "exactly `yes` or `no`"
	case channeladapter.InteractionChooseOne:
		grammar := "one option number (" + numberedOptions(field.Options) + ")"
		if field.AllowFreeform {
			grammar += " or `other=TEXT`"
		}
		return grammar
	case channeladapter.InteractionChooseMany:
		grammar := "comma-separated option numbers (" + numberedOptions(field.Options) + ")"
		if field.AllowFreeform {
			grammar += ", optionally followed by `;other=TEXT` (or `other=TEXT` alone)"
		}
		return grammar
	case channeladapter.InteractionText:
		return "the text value"
	case channeladapter.InteractionDateTime:
		switch field.DateTimeRepresentation {
		case channeladapter.DateOnly:
			return "a date in `YYYY-MM-DD` format"
		case channeladapter.TimeOnly:
			return "a time in 24-hour `HH:MM` format"
		default:
			return "an RFC 3339 date/time with an explicit offset"
		}
	default:
		return "a value"
	}
}

func numberedOptions(options []channeladapter.Option) string {
	parts := make([]string, len(options))
	for index, option := range options {
		parts[index] = fmt.Sprintf("%d=%s", index+1, option.Label)
	}
	return strings.Join(parts, ", ")
}

func (runtime *Runtime) controlResult(correlation string, result channeladapter.ControlResult) error {
	runtime.mu.Lock()
	pending, ok := runtime.controls[correlation]
	if ok {
		delete(runtime.controls, correlation)
	}
	runtime.mu.Unlock()
	if !ok {
		return errors.New("Discord adapter received an unknown control-result correlation")
	}
	if result.Action != pending.action || !runtime.writer.acknowledge(correlation) {
		return errors.New("Discord adapter received a mismatched control result")
	}
	content := "The channel request failed."
	switch result.Disposition {
	case channeladapter.ControlExact:
		if pending.action == channeladapter.ControlReset {
			content = "Started a new conversation."
		} else if result.Status != nil {
			status := result.Status
			content = fmt.Sprintf("hctl is online: agent=%s harness=%s state=%s pending=%d active=%d/%d resident=%d/%d queued=%d", status.Agent, status.Harness, status.State, status.Pending, status.Active, status.ActiveLimit, status.Resident, status.ResidentLimit, status.Queued)
		}
	case channeladapter.ControlBusy:
		content = "The conversation is busy. Try again after current work finishes."
	}
	_ = runtime.client.InteractionRespond(pending.interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: disabledMentions()}})
	return nil
}

func disabledMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func boolPointer(value bool) *bool { return &value }
