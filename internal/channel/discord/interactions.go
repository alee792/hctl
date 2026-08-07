package discord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/interaction"
)

const (
	componentPrefix      = "h1"
	maxFallbackChunks    = 20
	maxRememberedTargets = 128
)

func (r *Runtime) rememberTarget(inputID string, target replyTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.targets == nil {
		r.targets = map[string]replyTarget{}
	}
	if _, exists := r.targets[inputID]; !exists {
		r.targetOrder = append(r.targetOrder, inputID)
	}
	r.targets[inputID] = target
	for len(r.targetOrder) > maxRememberedTargets {
		oldest := r.targetOrder[0]
		r.targetOrder = r.targetOrder[1:]
		delete(r.targets, oldest)
	}
}

func (r *Runtime) recoverSurface(surfaceID string) {
	if surfaceID == "" {
		return
	}
	conversation := conversationID(r.config.Runtime.ApplicationID, surfaceID)
	pending, ok, err := r.controller.PendingInteraction(surfaceID, conversation)
	if err != nil || !ok {
		return
	}
	r.rememberTarget(pending.InputID, replyTarget{channelID: surfaceID, messageID: pending.InputID})
	_, _ = r.controller.RenderInteraction(surfaceID, conversation)
}

func (r *Runtime) forgetTarget(inputID string) {
	r.mu.Lock()
	delete(r.targets, inputID)
	r.mu.Unlock()
}

func (r *Runtime) Capabilities() interaction.Capabilities {
	return interaction.Capabilities{
		Kinds:               []interaction.Kind{interaction.KindConfirm, interaction.KindChooseOne, interaction.KindChooseMany, interaction.KindText, interaction.KindForm},
		FormFieldKinds:      []interaction.Kind{interaction.KindText},
		MaxRequestBytes:     interaction.MaxRequestBytes,
		MaxPromptBytes:      800,
		MaxFields:           5,
		MaxOptionsPerField:  25,
		MaxSelections:       25,
		MaxTotalOptions:     25,
		MaxLabelBytes:       45,
		MaxDescriptionBytes: 100,
		MaxValueBytes:       interaction.MaxValueBytes,
		MaxTextRunes:        interaction.MaxTextRunes,
		SupportsFreeform:    false,
	}
}

func (r *Runtime) Owner(surfaceID string) interaction.Owner {
	return interaction.Owner{
		SurfaceKey:   interaction.Digest("discord-surface\x00" + r.config.Runtime.ApplicationID + "\x00" + surfaceID),
		PrincipalKey: interaction.Digest("discord-principal\x00" + r.config.Runtime.ApplicationID + "\x00" + r.config.Runtime.AllowedUserID),
	}
}

func (r *Runtime) RecoverTarget(surfaceID, inputID string) (any, bool) {
	if surfaceID == "" || inputID == "" {
		return nil, false
	}
	return replyTarget{channelID: surfaceID, messageID: inputID}, true
}

func interactionHandle(interactionID string) string {
	sum := sha256.Sum256([]byte("hctl-discord-component-v1\x00" + interactionID))
	return hex.EncodeToString(sum[:12])
}

func customID(interactionID, action string) string {
	return componentPrefix + "." + interactionHandle(interactionID) + "." + action
}

func parseCustomID(value string) (handle, action string, ok bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != componentPrefix || len(parts[1]) != 24 || len(parts[2]) < 1 || len(parts[2]) > 4 {
		return "", "", false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", "", false
	}
	for _, char := range parts[2] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return "", "", false
		}
	}
	return parts[1], parts[2], true
}

func (r *Runtime) Render(_ context.Context, intent interaction.RenderIntent) interaction.EffectOutcome {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return interaction.EffectFailed
	}
	target, ok := r.targets[intent.InputID]
	delete(r.targets, intent.InputID)
	r.mu.Unlock()
	if !ok || target.channelID == "" || target.messageID == "" || intent.Owner != r.Owner(target.channelID) {
		return interaction.EffectFailed
	}
	if intent.Resolution.Mode == interaction.RenderTextFallback {
		return r.renderTextFallback(target, intent)
	}
	message, err := nativeMessage(target, intent)
	if err != nil {
		return interaction.EffectFailed
	}
	if err := r.deliver(target.channelID, message); err != nil {
		return interaction.EffectUncertain
	}
	return interaction.EffectSucceeded
}

func nativeMessage(target replyTarget, intent interaction.RenderIntent) (*discordgo.MessageSend, error) {
	request := intent.Request
	message := &discordgo.MessageSend{
		Content: request.Prompt, AllowedMentions: disabledMentions(),
		Reference: &discordgo.MessageReference{MessageID: target.messageID, ChannelID: target.channelID, FailIfNotExists: boolPointer(false)},
	}
	var rows []discordgo.MessageComponent
	addCancel := func(buttons []discordgo.MessageComponent) []discordgo.MessageComponent {
		if request.Policy.Cancellation == interaction.CancellationAllowed {
			buttons = append(buttons, discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: customID(intent.InteractionID, "x")})
		}
		return buttons
	}
	switch request.Kind {
	case interaction.KindConfirm:
		buttons := []discordgo.MessageComponent{
			discordgo.Button{Label: "Yes", Style: discordgo.SuccessButton, CustomID: customID(intent.InteractionID, "y")},
			discordgo.Button{Label: "No", Style: discordgo.DangerButton, CustomID: customID(intent.InteractionID, "n")},
		}
		rows = append(rows, discordgo.ActionsRow{Components: addCancel(buttons)})
	case interaction.KindChooseOne:
		if len(request.Field.Options)+boolInt(request.Policy.Cancellation == interaction.CancellationAllowed) <= 5 {
			buttons := make([]discordgo.MessageComponent, 0, len(request.Field.Options)+1)
			for i, option := range request.Field.Options {
				buttons = append(buttons, discordgo.Button{Label: option.Label, Style: discordgo.PrimaryButton, CustomID: customID(intent.InteractionID, "o"+strconv.Itoa(i))})
			}
			rows = append(rows, discordgo.ActionsRow{Components: addCancel(buttons)})
		} else {
			rows = append(rows, choiceSelect(intent, *request.Field), cancelRow(intent))
		}
	case interaction.KindChooseMany:
		rows = append(rows, choiceSelect(intent, *request.Field), cancelRow(intent))
	case interaction.KindText, interaction.KindForm:
		buttons := []discordgo.MessageComponent{discordgo.Button{Label: "Answer", Style: discordgo.PrimaryButton, CustomID: customID(intent.InteractionID, "a")}}
		rows = append(rows, discordgo.ActionsRow{Components: addCancel(buttons)})
	default:
		return nil, errors.New("unsupported Discord native interaction")
	}
	message.Components = compactRows(rows)
	if len(message.Content) > 2000 || len(message.Components) == 0 || len(message.Components) > 5 {
		return nil, errors.New("discord interaction exceeds native limits")
	}
	return message, nil
}

func choiceSelect(intent interaction.RenderIntent, field interaction.Field) discordgo.MessageComponent {
	options := make([]discordgo.SelectMenuOption, len(field.Options))
	for i, option := range field.Options {
		options[i] = discordgo.SelectMenuOption{Label: option.Label, Description: option.Description, Value: "v" + strconv.Itoa(i)}
	}
	minimum := field.MinSelections
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
		CustomID: customID(intent.InteractionID, "s"), Placeholder: field.Label,
		MinValues: &minimum, MaxValues: field.MaxSelections, Options: options,
	}}}
}

func cancelRow(intent interaction.RenderIntent) discordgo.MessageComponent {
	if intent.Request.Policy.Cancellation != interaction.CancellationAllowed {
		return nil
	}
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: customID(intent.InteractionID, "x")}}}
}

func compactRows(rows []discordgo.MessageComponent) []discordgo.MessageComponent {
	result := rows[:0]
	for _, row := range rows {
		if row != nil {
			result = append(result, row)
		}
	}
	return result
}

func (r *Runtime) renderTextFallback(target replyTarget, intent interaction.RenderIntent) interaction.EffectOutcome {
	content, err := interaction.TextFallback(intent.Request)
	if err != nil {
		return interaction.EffectFailed
	}
	marker := "[hctl request " + interactionHandle(intent.InteractionID) + "]"
	chunks := boundedChunks(content, marker, maxFallbackChunks)
	if chunks == nil {
		return interaction.EffectFailed
	}
	for _, chunk := range chunks {
		message := &discordgo.MessageSend{Content: chunk, AllowedMentions: disabledMentions(), Reference: &discordgo.MessageReference{MessageID: target.messageID, ChannelID: target.channelID, FailIfNotExists: boolPointer(false)}}
		if err := r.deliver(target.channelID, message); err != nil {
			return interaction.EffectUncertain
		}
	}
	return interaction.EffectSucceeded
}

func boundedChunks(content, marker string, maximum int) []string {
	suffix := "\n\n" + marker
	limit := 2000 - len([]rune(suffix))
	if limit <= 0 {
		return nil
	}
	runes := []rune(content)
	var chunks []string
	for len(runes) > 0 && len(chunks) < maximum {
		count := min(len(runes), limit)
		chunks = append(chunks, string(runes[:count])+suffix)
		runes = runes[count:]
	}
	if len(runes) != 0 || len(chunks) == 0 {
		return nil
	}
	return chunks
}

func (r *Runtime) handleFallbackReply(incoming *discordgo.MessageCreate) bool {
	if incoming.ReferencedMessage == nil || incoming.ReferencedMessage.Author == nil || incoming.ReferencedMessage.Author.ID != r.config.Runtime.BotUserID {
		return false
	}
	conversation := conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID)
	pending, ok, err := r.controller.PendingInteraction(incoming.ChannelID, conversation)
	if err != nil || !ok || pending.Resolution.Mode != interaction.RenderTextFallback || pending.Owner != r.Owner(incoming.ChannelID) {
		return false
	}
	marker := "[hctl request " + interactionHandle(pending.InteractionID) + "]"
	if !strings.Contains(incoming.ReferencedMessage.Content, marker) {
		return false
	}
	answer, err := interaction.ParseTextAnswer(pending.Request, incoming.Content)
	if err != nil {
		_ = r.deliver(incoming.ChannelID, &discordgo.MessageSend{
			Content: "That answer doesn't match the requested format. Reply again using the shown format.", AllowedMentions: disabledMentions(),
			Reference: &discordgo.MessageReference{MessageID: incoming.ID, ChannelID: incoming.ChannelID, FailIfNotExists: boolPointer(false)},
		})
		return true
	}
	disposition, err := r.controller.AcceptInteraction(incoming.ChannelID, conversation, interaction.AnswerAttempt{InteractionID: pending.InteractionID, Answer: answer})
	if err == nil && resumesInteraction(disposition) {
		_ = r.controller.ContinueInteraction(conversation)
	}
	return true
}

func (r *Runtime) handleComponent(incoming *discordgo.InteractionCreate) {
	context, ok := r.callbackContext(incoming)
	if !ok {
		return
	}
	var callbackID string
	if incoming.Type == discordgo.InteractionMessageComponent {
		data, valid := messageComponentData(incoming.Data)
		if !valid {
			return
		}
		callbackID = data.CustomID
	} else {
		data, valid := modalSubmitData(incoming.Data)
		if !valid {
			return
		}
		callbackID = data.CustomID
	}
	handle, action, ok := parseCustomID(callbackID)
	if !ok {
		return
	}
	pending, exists, err := r.controller.PendingInteraction(context.surface, context.conversation)
	if err != nil || !exists || pending.Owner != r.Owner(context.surface) || handle != interactionHandle(pending.InteractionID) {
		r.acknowledge(incoming.Interaction, "This request is no longer available.")
		return
	}
	if action == "a" && isButtonCallback(incoming) {
		modal, err := buildModal(pending)
		if err != nil {
			r.acknowledge(incoming.Interaction, "This request cannot be opened.")
			return
		}
		_ = r.respondNative(incoming.Interaction, modal)
		return
	}
	answer, err := decodeNativeAnswer(incoming, pending, action)
	if err != nil {
		r.acknowledge(incoming.Interaction, "That answer is invalid. Please try again.")
		return
	}
	disposition, err := r.controller.AcceptInteraction(context.surface, context.conversation, interaction.AnswerAttempt{InteractionID: pending.InteractionID, Answer: answer})
	if err != nil {
		r.acknowledge(incoming.Interaction, "This request is no longer available.")
		return
	}
	acknowledgement := "Answer received."
	if disposition == interaction.AnswerCancelled {
		acknowledgement = "Request cancelled."
	}
	r.acknowledge(incoming.Interaction, acknowledgement)
	if resumesInteraction(disposition) {
		_ = r.controller.ContinueInteraction(context.conversation)
	}
}

func resumesInteraction(disposition interaction.AnswerDisposition) bool {
	return disposition == interaction.AnswerAccepted || disposition == interaction.AnswerDuplicate
}

type discordCallbackContext struct{ surface, conversation string }

func (r *Runtime) callbackContext(incoming *discordgo.InteractionCreate) (discordCallbackContext, bool) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed || incoming.AppID != r.config.Runtime.ApplicationID {
		return discordCallbackContext{}, false
	}
	user := incoming.User
	if incoming.Member != nil && incoming.Member.User != nil {
		user = incoming.Member.User
	}
	if user == nil || user.Bot || user.ID != r.config.Runtime.AllowedUserID || incoming.ChannelID == "" {
		return discordCallbackContext{}, false
	}
	if incoming.GuildID != "" && (incoming.GuildID != r.config.Runtime.AllowedGuildID || incoming.ChannelID != r.config.Runtime.AllowedChannelID) {
		return discordCallbackContext{}, false
	}
	return discordCallbackContext{surface: incoming.ChannelID, conversation: conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID)}, true
}

func buildModal(intent interaction.PendingInteraction) (*discordgo.InteractionResponse, error) {
	fields := intent.Request.Fields
	if intent.Request.Field != nil {
		fields = []interaction.Field{*intent.Request.Field}
	}
	if len(fields) < 1 || len(fields) > 5 {
		return nil, errors.New("modal field count is invalid")
	}
	rows := make([]discordgo.MessageComponent, len(fields))
	for i, field := range fields {
		if field.Kind != interaction.KindText {
			return nil, errors.New("modal field kind is unsupported")
		}
		style := discordgo.TextInputShort
		if field.MaxLength > 200 {
			style = discordgo.TextInputParagraph
		}
		rows[i] = discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.TextInput{
			CustomID: customID(intent.InteractionID, "f"+strconv.Itoa(i)), Label: field.Label, Placeholder: field.Description,
			Style: style, Required: field.Required, MinLength: field.MinLength, MaxLength: field.MaxLength,
		}}}
	}
	return &discordgo.InteractionResponse{Type: discordgo.InteractionResponseModal, Data: &discordgo.InteractionResponseData{
		CustomID: customID(intent.InteractionID, "m"), Title: "Provide an answer", Components: rows,
	}}, nil
}

func decodeNativeAnswer(incoming *discordgo.InteractionCreate, intent interaction.PendingInteraction, action string) (interaction.Answer, error) {
	if action == "x" {
		if !isButtonCallback(incoming) {
			return interaction.Answer{}, errors.New("cancel callback is malformed")
		}
		return interaction.NormalizeAnswer(intent.Request, interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionCancel})
	}
	answer := interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit}
	field := intent.Request.Field
	switch {
	case isButtonCallback(incoming) && intent.Request.Kind == interaction.KindConfirm && (action == "y" || action == "n"):
		confirmed := action == "y"
		answer.Fields = []interaction.FieldAnswer{{FieldID: field.ID, Confirmed: &confirmed}}
	case isButtonCallback(incoming) && intent.Request.Kind == interaction.KindChooseOne && !choiceUsesSelect(intent.Request) && strings.HasPrefix(action, "o"):
		index, err := trustedIndex(strings.TrimPrefix(action, "o"), len(field.Options))
		if err != nil {
			return interaction.Answer{}, err
		}
		answer.Fields = []interaction.FieldAnswer{{FieldID: field.ID, OptionIDs: []string{field.Options[index].ID}}}
	case incoming.Type == discordgo.InteractionMessageComponent && (intent.Request.Kind == interaction.KindChooseMany || intent.Request.Kind == interaction.KindChooseOne && choiceUsesSelect(intent.Request)) && action == "s":
		data, ok := messageComponentData(incoming.Data)
		if !ok || data.ComponentType != discordgo.SelectMenuComponent {
			return interaction.Answer{}, errors.New("select callback is malformed")
		}
		selected := make([]string, 0, len(data.Values))
		seen := map[int]bool{}
		for _, value := range data.Values {
			if !strings.HasPrefix(value, "v") {
				return interaction.Answer{}, errors.New("select value is malformed")
			}
			index, err := trustedIndex(strings.TrimPrefix(value, "v"), len(field.Options))
			if err != nil || seen[index] {
				return interaction.Answer{}, errors.New("select value is invalid")
			}
			seen[index] = true
			selected = append(selected, field.Options[index].ID)
		}
		answer.Fields = []interaction.FieldAnswer{{FieldID: field.ID, OptionIDs: selected}}
	case incoming.Type == discordgo.InteractionModalSubmit && action == "m" && (intent.Request.Kind == interaction.KindText || intent.Request.Kind == interaction.KindForm):
		fields := intent.Request.Fields
		if field != nil {
			fields = []interaction.Field{*field}
		}
		values, err := modalValues(incoming.Data, intent.InteractionID, len(fields))
		if err != nil {
			return interaction.Answer{}, err
		}
		for i, semanticField := range fields {
			value := values[i]
			answer.Fields = append(answer.Fields, interaction.FieldAnswer{FieldID: semanticField.ID, Text: &value})
		}
	default:
		return interaction.Answer{}, errors.New("callback action does not match request")
	}
	return interaction.NormalizeAnswer(intent.Request, answer)
}

func choiceUsesSelect(request interaction.Request) bool {
	return request.Kind == interaction.KindChooseMany || request.Kind == interaction.KindChooseOne && len(request.Field.Options)+boolInt(request.Policy.Cancellation == interaction.CancellationAllowed) > 5
}

func messageComponentData(data discordgo.InteractionData) (discordgo.MessageComponentInteractionData, bool) {
	switch value := data.(type) {
	case discordgo.MessageComponentInteractionData:
		return value, true
	case *discordgo.MessageComponentInteractionData:
		if value != nil {
			return *value, true
		}
	}
	return discordgo.MessageComponentInteractionData{}, false
}

func modalSubmitData(data discordgo.InteractionData) (discordgo.ModalSubmitInteractionData, bool) {
	switch value := data.(type) {
	case discordgo.ModalSubmitInteractionData:
		return value, true
	case *discordgo.ModalSubmitInteractionData:
		if value != nil {
			return *value, true
		}
	}
	return discordgo.ModalSubmitInteractionData{}, false
}

func isButtonCallback(incoming *discordgo.InteractionCreate) bool {
	if incoming == nil || incoming.Type != discordgo.InteractionMessageComponent {
		return false
	}
	data, ok := messageComponentData(incoming.Data)
	return ok && data.ComponentType == discordgo.ButtonComponent
}

func modalValues(data discordgo.InteractionData, interactionID string, count int) ([]string, error) {
	var modal discordgo.ModalSubmitInteractionData
	switch value := data.(type) {
	case discordgo.ModalSubmitInteractionData:
		modal = value
	case *discordgo.ModalSubmitInteractionData:
		if value == nil {
			return nil, errors.New("modal callback is malformed")
		}
		modal = *value
	default:
		return nil, errors.New("modal callback is malformed")
	}
	values := make([]string, count)
	seen := make([]bool, count)
	for _, rawRow := range modal.Components {
		row, ok := rawRow.(*discordgo.ActionsRow)
		if !ok {
			if value, valueOK := rawRow.(discordgo.ActionsRow); valueOK {
				row = &value
				ok = true
			}
		}
		if !ok || len(row.Components) != 1 {
			return nil, errors.New("modal row is malformed")
		}
		var input discordgo.TextInput
		switch value := row.Components[0].(type) {
		case discordgo.TextInput:
			input = value
		case *discordgo.TextInput:
			if value == nil {
				return nil, errors.New("modal input is malformed")
			}
			input = *value
		default:
			return nil, errors.New("modal input is malformed")
		}
		handle, action, valid := parseCustomID(input.CustomID)
		if !valid || handle != interactionHandle(interactionID) || !strings.HasPrefix(action, "f") {
			return nil, errors.New("modal input is not correlated")
		}
		index, err := trustedIndex(strings.TrimPrefix(action, "f"), count)
		if err != nil || seen[index] {
			return nil, errors.New("modal input is invalid")
		}
		seen[index] = true
		values[index] = input.Value
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("modal input is missing")
		}
	}
	return values, nil
}

func trustedIndex(value string, count int) (int, error) {
	index, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(index) != value || index < 0 || index >= count {
		return 0, errors.New("component slot is invalid")
	}
	return index, nil
}

func (r *Runtime) acknowledge(incoming *discordgo.Interaction, content string) {
	if r.respondNative == nil {
		return
	}
	_ = r.respondNative(incoming, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{
		Content: content, Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: disabledMentions(),
	}})
}

func disabledMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func boolPointer(value bool) *bool { return &value }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
