package discordadapter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

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
	message, err := interactionMessage(pending)
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
	content := request.Prompt + "\n\n" + request.FallbackText + "\n\n[hctl request " + pending.handle + "]"
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
		frameID, err := runtime.writer.sendEvent("control:"+incoming.ID, channeladapter.ControlRequest{SourceID: incoming.ID, Route: channeladapter.Route{Handle: incoming.ChannelID}, Message: channeladapter.MessageRef{Handle: incoming.ID}, Action: action}, "")
		if err == nil {
			runtime.mu.Lock()
			runtime.controls[frameID] = pendingControl{interaction: incoming.Interaction, action: action}
			runtime.mu.Unlock()
		}
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
	runtime.finishInteraction(pending, answer)
	message := "Answer received."
	if answer.Action == channeladapter.AnswerCancel {
		message = "Request cancelled."
	}
	_ = runtime.client.InteractionRespond(incoming.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseUpdateMessage, Data: &discordgo.InteractionResponseData{Content: message, AllowedMentions: disabledMentions()}})
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

func (runtime *Runtime) finishInteraction(pending pendingInteraction, answer channeladapter.SemanticInteractionAnswer) {
	runtime.mu.Lock()
	delete(runtime.interactions, pending.request.InteractionID)
	delete(runtime.handles, pending.handle)
	runtime.mu.Unlock()
	_, _ = runtime.writer.sendEvent("interaction:"+pending.request.InteractionID, channeladapter.InteractionResult{InteractionID: pending.request.InteractionID, Answer: answer}, pending.hostFrameID)
}

func (runtime *Runtime) cancelInteraction(hostFrameID string, cancel channeladapter.InteractionCancel) {
	runtime.mu.Lock()
	pending, ok := runtime.interactions[cancel.InteractionID]
	runtime.mu.Unlock()
	if !ok {
		return
	}
	pending.hostFrameID = hostFrameID
	runtime.finishInteraction(pending, channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerCancel})
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
	runtime.finishInteraction(pending, answer)
	return true
}

func fallbackAnswer(request channeladapter.SemanticInteractionRequest, content string) (channeladapter.SemanticInteractionAnswer, error) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerSubmit}
	if request.Policy.Cancellation == channeladapter.CancellationAllowed && strings.EqualFold(content, "cancel") {
		answer.Action = channeladapter.AnswerCancel
		return answer, nil
	}
	switch request.Kind {
	case channeladapter.InteractionConfirm:
		value := strings.EqualFold(content, "yes")
		if !value && !strings.EqualFold(content, "no") {
			return answer, errors.New("expected yes or no")
		}
		answer.Fields = []channeladapter.FieldAnswer{{FieldID: request.Field.ID, Confirmed: &value}}
	case channeladapter.InteractionText:
		answer.Fields = []channeladapter.FieldAnswer{{FieldID: request.Field.ID, Text: &content}}
	case channeladapter.InteractionChooseOne, channeladapter.InteractionChooseMany:
		parts := strings.Split(content, ",")
		var ids []string
		for _, part := range parts {
			index, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || index < 1 || index > len(request.Field.Options) {
				return answer, errors.New("invalid choice ordinal")
			}
			ids = append(ids, request.Field.Options[index-1].ID)
		}
		answer.Fields = []channeladapter.FieldAnswer{{FieldID: request.Field.ID, OptionIDs: ids}}
	default:
		return answer, errors.New("unsupported fallback shape")
	}
	return answer, nil
}

func (runtime *Runtime) controlResult(correlation string, result channeladapter.ControlResult) {
	runtime.writer.acknowledge(correlation)
	runtime.mu.Lock()
	pending, ok := runtime.controls[correlation]
	delete(runtime.controls, correlation)
	runtime.mu.Unlock()
	if !ok {
		return
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
}

func disabledMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func boolPointer(value bool) *bool { return &value }
