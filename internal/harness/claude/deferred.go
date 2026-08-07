package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"hctl/internal/interaction"
)

const (
	ManagedRequestInputTool = "mcp__managed__channel.request_input"
	DeferredBrokerEnv       = "HCTL_CLAUDE_DEFERRED_BROKER"
	maxHookInputBytes       = 64 << 10
)

var (
	ErrDeferredUnavailable = errors.New("claude deferred tool is unavailable")
	ErrDeferredMismatch    = errors.New("claude deferred tool correlation mismatch")
	ErrDeferredParallel    = errors.New("claude parallel tool calls cannot be deferred")
	ErrDeferredSessionLost = errors.New("claude retained deferred session is unavailable")
	ErrDeferredDelivery    = errors.New("claude deferred broker delivery is uncertain")
)

type deferredResumeEnvelope struct {
	ToolUseID    string          `json:"tool_use_id"`
	ToolName     string          `json:"tool_name"`
	InputDigest  string          `json:"input_digest"`
	UpdatedInput json.RawMessage `json:"updated_input"`
}

type hookInput struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolUseID     string          `json:"tool_use_id"`
	ToolInput     json.RawMessage `json:"tool_input"`
	AgentID       string          `json:"agent_id"`
}

type deferredToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func RequestDigest(input []byte) (string, error) {
	request, err := interaction.DecodeRequest(input)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return "", errors.New("cannot encode deferred tool input")
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func BuildDeferredUpdatedInput(request interaction.Request, answer interaction.Answer, toolUseID string) ([]byte, string, error) {
	if !validToolUseID(toolUseID) {
		return nil, "", errors.New("claude deferred tool id is invalid")
	}
	normalized, err := interaction.NormalizeAnswer(request, answer)
	if err != nil {
		return nil, "", errors.New("claude deferred answer is invalid")
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return nil, "", errors.New("cannot encode Claude deferred request")
	}
	digest, err := RequestDigest(requestBytes)
	if err != nil {
		return nil, "", err
	}
	var fields map[string]any
	if err := json.Unmarshal(requestBytes, &fields); err != nil {
		return nil, "", errors.New("cannot encode Claude deferred request")
	}
	fields["_hctl_response"] = map[string]any{"tool_use_id": toolUseID, "answer": normalized}
	updated, err := json.Marshal(fields)
	if err != nil || len(updated) > interaction.MaxRequestBytes+interaction.MaxAnswerBytes+4096 {
		return nil, "", errors.New("claude deferred updated input exceeds its bound")
	}
	// The hook protocol carries updatedInput as a JSON value, so Claude will
	// serialize it again before invoking MCP. Canonicalize every nested object
	// now to make the broker's strict comparison stable across that exact
	// hook-to-tool round trip.
	var canonical any
	if json.Unmarshal(updated, &canonical) != nil {
		return nil, "", errors.New("cannot canonicalize Claude deferred input")
	}
	updated, err = json.Marshal(canonical)
	if err != nil || len(updated) > interaction.MaxRequestBytes+interaction.MaxAnswerBytes+4096 {
		return nil, "", errors.New("claude deferred updated input exceeds its bound")
	}
	return updated, digest, nil
}

func validateDeferredResume(toolUseID, inputDigest string, updatedInput []byte) (deferredResumeEnvelope, error) {
	envelope := deferredResumeEnvelope{ToolUseID: toolUseID, ToolName: ManagedRequestInputTool, InputDigest: inputDigest, UpdatedInput: updatedInput}
	digestBytes, digestErr := hex.DecodeString(inputDigest)
	if !validToolUseID(toolUseID) || digestErr != nil || len(digestBytes) != sha256.Size || len(updatedInput) == 0 || len(updatedInput) > interaction.MaxRequestBytes+interaction.MaxAnswerBytes+4096 {
		return deferredResumeEnvelope{}, errors.New("invalid Claude deferred resume envelope")
	}
	return envelope, nil
}

// decodeDeferredToolResult validates the MCP call produced after the hook
// allows an exact deferred invocation. It returns only the normalized semantic
// answer that the original Claude turn should observe.
func decodeDeferredToolResult(arguments []byte, resume deferredResumeEnvelope) (interaction.Answer, error) {
	var zero interaction.Answer
	if resume.ToolName != ManagedRequestInputTool || !validToolUseID(resume.ToolUseID) {
		return zero, errors.New("claude deferred resume state is invalid")
	}
	actual, err := canonicalStrictJSON(arguments)
	if err != nil {
		return zero, errors.New("claude deferred result input is invalid")
	}
	expected, err := canonicalStrictJSON(resume.UpdatedInput)
	if err != nil || !bytes.Equal(actual, expected) {
		return zero, errors.New("claude deferred result did not match the allowed tool call")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return zero, errors.New("claude deferred result input is invalid")
	}
	responseBytes, ok := fields["_hctl_response"]
	if !ok {
		return zero, errors.New("claude deferred result response is missing")
	}
	delete(fields, "_hctl_response")
	requestBytes, err := json.Marshal(fields)
	if err != nil {
		return zero, errors.New("claude deferred request input is invalid")
	}
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		return zero, errors.New("claude deferred request input is invalid")
	}
	digest, err := RequestDigest(requestBytes)
	if err != nil || digest != resume.InputDigest {
		return zero, errors.New("claude deferred request changed before execution")
	}
	var response struct {
		ToolUseID string             `json:"tool_use_id"`
		Answer    interaction.Answer `json:"answer"`
	}
	if err := decodeBounded(responseBytes, interaction.MaxAnswerBytes+512, &response); err != nil || response.ToolUseID != resume.ToolUseID {
		return zero, errors.New("claude deferred response correlation is invalid")
	}
	normalized, err := interaction.NormalizeAnswer(request, response.Answer)
	if err != nil {
		return zero, errors.New("claude deferred response answer is invalid")
	}
	return normalized, nil
}

func canonicalStrictJSON(value []byte) ([]byte, error) {
	if len(value) == 0 || len(value) > interaction.MaxRequestBytes+interaction.MaxAnswerBytes+4096 || !utf8.Valid(value) {
		return nil, errors.New("JSON value is outside its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds its bound")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("JSON object key is duplicated")
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("JSON delimiter is invalid")
		}
	}
	if err := walk(0); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values are not allowed")
	}
	var decoded any
	canonicalDecoder := json.NewDecoder(bytes.NewReader(value))
	canonicalDecoder.UseNumber()
	if err := canonicalDecoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

// RunDeferredHook implements the narrowly matched PreToolUse command hook.
// The absence of resume state means the first invocation must park. Resume
// state is supplied ephemerally by the parent hctl process.
func RunDeferredHook(input io.Reader, output io.Writer, brokerPath string) error {
	data, err := io.ReadAll(io.LimitReader(input, maxHookInputBytes+1))
	if err != nil || len(data) > maxHookInputBytes {
		return writeDeny(output)
	}
	response, err := brokerRoundTrip(brokerPath, brokerRequest{Kind: "hook", Value: data})
	if err != nil || len(response.Hook) == 0 {
		return writeDeny(output)
	}
	_, err = output.Write(append(response.Hook, '\n'))
	return err
}

func writeDeny(output io.Writer) error {
	return json.NewEncoder(output).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": "interactive input request was rejected",
	}})
}

func parseDeferredTool(value any) (deferredToolUse, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > interaction.MaxRequestBytes+1024 {
		return deferredToolUse{}, errors.New("claude deferred tool payload is invalid")
	}
	var deferred deferredToolUse
	if err := decodeBounded(data, interaction.MaxRequestBytes+1024, &deferred); err != nil ||
		!validToolUseID(deferred.ID) || deferred.Name != ManagedRequestInputTool {
		return deferredToolUse{}, errors.New("claude deferred an unexpected tool")
	}
	if _, err := interaction.DecodeRequest(deferred.Input); err != nil {
		return deferredToolUse{}, errors.New("claude deferred tool input is invalid")
	}
	return deferred, nil
}

func validToolUseID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func decodeBounded(data []byte, limit int, target any) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New("JSON value is outside its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}
