package dispatchstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"hctl/internal/interaction"
	"hctl/internal/rootfs"
)

const (
	statePath = ".hctl/dispatch.json"
	// legacyStatePath is read only to migrate workspaces created before the
	// turn dispatcher terminology replaced hctl's internal gateway name.
	legacyStatePath  = ".hctl/gateway.json"
	maxStateBytes    = 1 << 20
	maxQueue         = 32
	maxRecentOutcome = 256
)

type State struct {
	SchemaVersion int                      `json:"schema_version"`
	Conversations map[string]*Conversation `json:"conversations"`
}

type Conversation struct {
	ID                        string                  `json:"id"`
	AgentID                   string                  `json:"agent_id,omitempty"`
	Harness                   string                  `json:"harness"`
	SourceFingerprint         string                  `json:"source_fingerprint,omitempty"`
	LegacyManifestFingerprint string                  `json:"manifest_fingerprint,omitempty"`
	SessionID                 string                  `json:"session_id,omitempty"`
	WorkspaceRoot             string                  `json:"workspace_root,omitempty"`
	WorktreeBranch            string                  `json:"worktree_branch,omitempty"`
	WorktreeRetiring          bool                    `json:"worktree_retiring,omitempty"`
	Queue                     []Input                 `json:"queue"`
	Outcomes                  map[string]string       `json:"outcomes"`
	OutcomeOrder              []string                `json:"outcome_order"`
	OutcomeReasons            map[string]string       `json:"outcome_reasons,omitempty"`
	Interaction               *interaction.Lifecycle  `json:"interaction,omitempty"`
	InteractionTombstones     []interaction.Tombstone `json:"interaction_tombstones,omitempty"`
	InteractionWakePending    bool                    `json:"interaction_wake_pending,omitempty"`
}

type Input struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

const (
	inputQueued = "queued"
	inputActive = "active"
	inputParked = "parked"

	// OutcomeReasonDeadlineExceeded distinguishes a confirmed task deadline
	// from an otherwise ambiguous uncertain outcome.
	OutcomeReasonDeadlineExceeded = "deadline_exceeded"
)

func Load(root string) (*State, error) {
	data, mode, exists, err := rootfs.ReadOptional(root, statePath, maxStateBytes)
	if err != nil {
		return nil, errors.New("dispatch state must be a small regular file")
	}
	if !exists {
		data, mode, exists, err = rootfs.ReadOptional(root, legacyStatePath, maxStateBytes)
		if err != nil {
			return nil, errors.New("dispatch state must be a small regular file")
		}
		if !exists {
			return &State{SchemaVersion: 4, Conversations: map[string]*Conversation{}}, nil
		}
		state, err := decode(data, mode)
		if err != nil {
			return nil, err
		}
		// Install the already validated bytes atomically before removing the old
		// path. Do not call Save here: schema-version migration needs the agent
		// identity supplied later by GetOrCreate.
		if err := rootfs.WriteAtomic(root, statePath, data, 0o600); err != nil {
			return nil, errors.New("cannot persist migrated dispatch state")
		}
		if err := rootfs.RemoveRegular(root, legacyStatePath); err != nil {
			return nil, errors.New("cannot remove migrated dispatch state")
		}
		return state, nil
	}
	return decode(data, mode)
}

func decode(data []byte, mode os.FileMode) (*State, error) {
	if mode.Perm()&0o077 != 0 {
		return nil, errors.New("dispatch state permissions are too broad; require owner-only access")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, errors.New("dispatch state is invalid")
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || (state.SchemaVersion < 1 || state.SchemaVersion > 4) || state.Conversations == nil {
		return nil, errors.New("dispatch state is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("dispatch state is invalid")
	}
	for key, conversation := range state.Conversations {
		if conversation != nil && conversation.LegacyManifestFingerprint != "" {
			if conversation.SourceFingerprint != "" && conversation.SourceFingerprint != conversation.LegacyManifestFingerprint {
				return nil, errors.New("dispatch conversation source fingerprints conflict")
			}
			conversation.SourceFingerprint = conversation.LegacyManifestFingerprint
			conversation.LegacyManifestFingerprint = ""
		}
		if conversation == nil {
			return nil, errors.New("dispatch conversation state is invalid")
		}
		expectedKey := conversationKey(conversation.AgentID, conversation.Harness, conversation.ID, conversation.SourceFingerprint)
		if state.SchemaVersion == 1 && conversation.AgentID == "" {
			expectedKey = conversation.Harness + ":" + conversation.ID
		}
		if key != expectedKey || (state.SchemaVersion >= 2 && conversation.AgentID == "") || (conversation.Harness != "claude" && conversation.Harness != "codex") || len(conversation.Queue) > maxQueue || len(conversation.OutcomeOrder) > maxRecentOutcome || len(conversation.InteractionTombstones) > interaction.MaxTerminalTombstones {
			return nil, errors.New("dispatch conversation state is invalid")
		}
		if state.SchemaVersion < 3 && (conversation.Interaction != nil || len(conversation.InteractionTombstones) != 0) {
			return nil, errors.New("dispatch interaction state requires schema version 3")
		}
		if state.SchemaVersion < 4 && len(conversation.OutcomeReasons) != 0 {
			return nil, errors.New("dispatch outcome reasons require schema version 4")
		}
		if err := (interaction.DurableState{Pending: conversation.Interaction, Tombstones: conversation.InteractionTombstones}).Validate(); err != nil {
			return nil, errors.New("dispatch interaction state is invalid")
		}
		if conversation.SourceFingerprint == "" {
			return nil, errors.New("dispatch conversation source fingerprint is missing")
		}
		if (conversation.WorkspaceRoot == "") != (conversation.WorktreeBranch == "") || conversation.WorktreeRetiring && conversation.WorkspaceRoot == "" || (conversation.WorkspaceRoot != "" && (!filepath.IsAbs(conversation.WorkspaceRoot) || filepath.Clean(conversation.WorkspaceRoot) != conversation.WorkspaceRoot || len(conversation.WorkspaceRoot) > 4096 || len(conversation.WorktreeBranch) > 255 || strings.ContainsAny(conversation.WorktreeBranch, "\x00\r\n"))) {
			return nil, errors.New("dispatch conversation worktree assignment is invalid")
		}
		if conversation.Outcomes == nil {
			conversation.Outcomes = map[string]string{}
		}
		if conversation.OutcomeReasons == nil {
			conversation.OutcomeReasons = map[string]string{}
		}
		seen := map[string]bool{}
		for index, input := range conversation.Queue {
			validStatus := input.Status == inputQueued || input.Status == inputActive || state.SchemaVersion >= 3 && input.Status == inputParked
			if input.ID == "" || input.Text == "" || !validStatus || seen[input.ID] || input.Status == inputParked && index != 0 {
				return nil, errors.New("dispatch queue state is invalid")
			}
			seen[input.ID] = true
		}
		if conversation.Interaction != nil {
			if len(conversation.Queue) == 0 || conversation.Queue[0].Status != inputParked || conversation.Queue[0].ID != conversation.Interaction.InputID {
				return nil, errors.New("dispatch interaction origin is invalid")
			}
		} else if len(conversation.Queue) > 0 && conversation.Queue[0].Status == inputParked {
			return nil, errors.New("dispatch parked input is missing its interaction")
		}
		if conversation.InteractionWakePending && (conversation.Interaction != nil || len(conversation.Queue) == 0 || conversation.Queue[0].Status != inputQueued) {
			return nil, errors.New("dispatch interaction wake state is invalid")
		}
		for _, id := range conversation.OutcomeOrder {
			if id == "" || seen[id] || conversation.Outcomes[id] == "" {
				return nil, errors.New("dispatch outcome state is invalid")
			}
			seen[id] = true
		}
		if len(conversation.Outcomes) != len(conversation.OutcomeOrder) {
			return nil, errors.New("dispatch outcome state is invalid")
		}
		for id, reason := range conversation.OutcomeReasons {
			if conversation.Outcomes[id] != "uncertain" || reason != OutcomeReasonDeadlineExceeded {
				return nil, errors.New("dispatch outcome reason state is invalid")
			}
		}
	}
	return &state, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
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
			seen := make(map[string]struct{})
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
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("JSON delimiter is invalid")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains multiple values")
	}
	return nil
}

func Save(root string, state *State) error {
	state.SchemaVersion = 4
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.New("cannot encode dispatch state")
	}
	if len(data) > maxStateBytes {
		return errors.New("dispatch state exceeds its 1 MiB limit")
	}
	return rootfs.WriteAtomic(root, statePath, append(data, '\n'), 0o600)
}

func (s *State) GetOrCreate(agentID, harness, id, fingerprint string) (*Conversation, error) {
	key := conversationKey(agentID, harness, id, fingerprint)
	conversation := s.Conversations[key]
	if conversation == nil && s.SchemaVersion == 1 {
		if len(s.Conversations) != 1 {
			return nil, errors.New("legacy dispatch state cannot be assigned to an agent unambiguously")
		}
		legacyKey := harness + ":" + id
		legacy := s.Conversations[legacyKey]
		if legacy == nil || legacy.SourceFingerprint != fingerprint {
			return nil, errors.New("legacy dispatch conversation does not match the selected agent source")
		}
		delete(s.Conversations, legacyKey)
		legacy.AgentID = agentID
		conversation = legacy
		s.Conversations[key] = conversation
		s.SchemaVersion = 2
	}
	if conversation == nil {
		conversation = &Conversation{ID: id, AgentID: agentID, Harness: harness, SourceFingerprint: fingerprint, Outcomes: map[string]string{}}
		s.Conversations[key] = conversation
	}
	if conversation.AgentID != agentID || conversation.Harness != harness || conversation.ID != id || conversation.SourceFingerprint != fingerprint {
		return nil, errors.New("conversation mapping belongs to a different harness or source; choose a new conversation id")
	}
	return conversation, nil
}

// Reset removes one idle durable conversation so the next submission opens a
// fresh native harness session. Callers must stop its dispatcher first.
func (s *State) Reset(agentID, harness, id, fingerprint string) {
	delete(s.Conversations, conversationKey(agentID, harness, id, fingerprint))
}

// ResetLifecycle starts a fresh native session without discarding an isolated
// workspace already assigned to the external conversation.
func (c *Conversation) ResetLifecycle() {
	c.SessionID = ""
	c.Queue = nil
	c.Outcomes = map[string]string{}
	c.OutcomeOrder = nil
	c.OutcomeReasons = map[string]string{}
	c.InteractionWakePending = false
}

func conversationKey(agentID, harness, id, fingerprint string) string {
	return agentID + ":" + fingerprint + ":" + harness + ":" + id
}

func (c *Conversation) Accept(id, text string) (string, bool, error) {
	for _, input := range c.Queue {
		if input.ID == id {
			return input.Status, true, nil
		}
	}
	if outcome := c.Outcomes[id]; outcome != "" {
		return outcome, true, nil
	}
	if len(c.Queue) >= maxQueue {
		return "", false, errors.New("queue_full")
	}
	c.Queue = append(c.Queue, Input{ID: id, Text: text, Status: inputQueued})
	return inputQueued, false, nil
}

func (c *Conversation) StartNext() (Input, error) {
	if len(c.Queue) == 0 {
		return Input{}, errors.New("queue is empty")
	}
	if c.Queue[0].Status != inputQueued {
		return Input{}, errors.New("next input is not queued")
	}
	c.Queue[0].Status = inputActive
	return c.Queue[0], nil
}

func (c *Conversation) Complete(id, outcome string) error {
	return c.CompleteWithReason(id, outcome, "")
}

func (c *Conversation) CompleteWithReason(id, outcome, reason string) error {
	if len(c.Queue) == 0 || c.Queue[0].ID != id || c.Queue[0].Status != inputActive {
		return fmt.Errorf("active input %s does not match durable queue", id)
	}
	if reason != "" && (outcome != "uncertain" || reason != OutcomeReasonDeadlineExceeded) {
		return errors.New("unsupported dispatch outcome reason")
	}
	c.Queue = c.Queue[1:]
	c.remember(id, outcome, reason)
	return nil
}

// Park marks the active queue head as durably waiting for an external answer.
// The caller must persist the matching interaction in the same transaction.
func (c *Conversation) Park(id string) error {
	if len(c.Queue) == 0 || c.Queue[0].ID != id || c.Queue[0].Status != inputActive {
		return errors.New("active input does not match durable queue")
	}
	c.Queue[0].Status = inputParked
	return nil
}

// CompleteParked removes a parked queue head after its interaction reaches a
// terminal state. The caller must clear the interaction in the same transaction.
func (c *Conversation) CompleteParked(id, outcome string) error {
	if len(c.Queue) == 0 || c.Queue[0].ID != id || c.Queue[0].Status != inputParked {
		return errors.New("parked input does not match durable queue")
	}
	c.Queue = c.Queue[1:]
	c.remember(id, outcome, "")
	return nil
}

func (c *Conversation) RecoverUncertain() []string {
	var uncertain []string
	kept := c.Queue[:0]
	for _, input := range c.Queue {
		if input.Status == inputActive {
			uncertain = append(uncertain, input.ID)
			c.remember(input.ID, "uncertain", "")
			continue
		}
		kept = append(kept, input)
	}
	c.Queue = kept
	return uncertain
}

// RecoverTaskUncertain terminalizes every non-parked task left in the durable
// queue. A queued entry may already have been externally announced before a
// crash, so restart never silently executes it.
func (c *Conversation) RecoverTaskUncertain() ([]string, error) {
	var recovered []string
	for len(c.Queue) > 0 {
		if c.Queue[0].Status == inputParked {
			return nil, errors.New("task conversation cannot contain parked input")
		}
		if c.Queue[0].Status == inputQueued {
			if _, err := c.StartNext(); err != nil {
				return nil, err
			}
		}
		id := c.Queue[0].ID
		if err := c.Complete(id, "uncertain"); err != nil {
			return nil, err
		}
		recovered = append(recovered, id)
	}
	c.SessionID = ""
	return recovered, nil
}

// OutcomeReason returns the optional bounded classification for a retained
// terminal outcome.
func (c *Conversation) OutcomeReason(id string) string {
	return c.OutcomeReasons[id]
}

func (c *Conversation) remember(id, outcome, reason string) {
	if c.Outcomes == nil {
		c.Outcomes = map[string]string{}
	}
	if c.OutcomeReasons == nil {
		c.OutcomeReasons = map[string]string{}
	}
	if c.Outcomes[id] == "" {
		c.OutcomeOrder = append(c.OutcomeOrder, id)
	}
	c.Outcomes[id] = outcome
	if reason == "" {
		delete(c.OutcomeReasons, id)
	} else {
		c.OutcomeReasons[id] = reason
	}
	for len(c.OutcomeOrder) > maxRecentOutcome {
		oldest := c.OutcomeOrder[0]
		c.OutcomeOrder = c.OutcomeOrder[1:]
		delete(c.Outcomes, oldest)
		delete(c.OutcomeReasons, oldest)
	}
}
