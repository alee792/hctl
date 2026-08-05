package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"hctl/internal/rootfs"
)

const (
	statePath        = ".hctl/gateway.json"
	maxStateBytes    = 1 << 20
	maxQueue         = 32
	maxRecentOutcome = 256
)

type State struct {
	SchemaVersion int                      `json:"schema_version"`
	Conversations map[string]*Conversation `json:"conversations"`
}

type Conversation struct {
	ID                        string            `json:"id"`
	AgentID                   string            `json:"agent_id,omitempty"`
	Harness                   string            `json:"harness"`
	SourceFingerprint         string            `json:"source_fingerprint,omitempty"`
	LegacyManifestFingerprint string            `json:"manifest_fingerprint,omitempty"`
	SessionID                 string            `json:"session_id,omitempty"`
	Queue                     []Input           `json:"queue"`
	Outcomes                  map[string]string `json:"outcomes"`
	OutcomeOrder              []string          `json:"outcome_order"`
}

type Input struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

func Load(root string) (*State, error) {
	data, mode, exists, err := rootfs.ReadOptional(root, statePath, maxStateBytes)
	if err != nil {
		return nil, errors.New("gateway state must be a small regular file")
	}
	if !exists {
		return &State{SchemaVersion: 2, Conversations: map[string]*Conversation{}}, nil
	}
	if mode.Perm()&0o077 != 0 {
		return nil, errors.New("gateway state permissions are too broad; require owner-only access")
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || (state.SchemaVersion != 1 && state.SchemaVersion != 2) || state.Conversations == nil {
		return nil, errors.New("gateway state is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("gateway state is invalid")
	}
	for key, conversation := range state.Conversations {
		if conversation != nil && conversation.LegacyManifestFingerprint != "" {
			if conversation.SourceFingerprint != "" && conversation.SourceFingerprint != conversation.LegacyManifestFingerprint {
				return nil, errors.New("gateway conversation source fingerprints conflict")
			}
			conversation.SourceFingerprint = conversation.LegacyManifestFingerprint
			conversation.LegacyManifestFingerprint = ""
		}
		if conversation == nil {
			return nil, errors.New("gateway conversation state is invalid")
		}
		expectedKey := conversationKey(conversation.AgentID, conversation.Harness, conversation.ID, conversation.SourceFingerprint)
		if state.SchemaVersion == 1 && conversation.AgentID == "" {
			expectedKey = conversation.Harness + ":" + conversation.ID
		}
		if key != expectedKey || (state.SchemaVersion == 2 && conversation.AgentID == "") || (conversation.Harness != "claude" && conversation.Harness != "codex") || len(conversation.Queue) > maxQueue || len(conversation.OutcomeOrder) > maxRecentOutcome {
			return nil, errors.New("gateway conversation state is invalid")
		}
		if conversation.SourceFingerprint == "" {
			return nil, errors.New("gateway conversation source fingerprint is missing")
		}
		if conversation.Outcomes == nil {
			conversation.Outcomes = map[string]string{}
		}
		seen := map[string]bool{}
		for _, input := range conversation.Queue {
			if input.ID == "" || input.Text == "" || (input.Status != "queued" && input.Status != "active") || seen[input.ID] {
				return nil, errors.New("gateway queue state is invalid")
			}
			seen[input.ID] = true
		}
		for _, id := range conversation.OutcomeOrder {
			if id == "" || seen[id] || conversation.Outcomes[id] == "" {
				return nil, errors.New("gateway outcome state is invalid")
			}
			seen[id] = true
		}
		if len(conversation.Outcomes) != len(conversation.OutcomeOrder) {
			return nil, errors.New("gateway outcome state is invalid")
		}
	}
	return &state, nil
}

func Save(root string, state *State) error {
	state.SchemaVersion = 2
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.New("cannot encode gateway state")
	}
	if len(data) > maxStateBytes {
		return errors.New("gateway state exceeds its 1 MiB limit")
	}
	return rootfs.WriteAtomic(root, statePath, append(data, '\n'), 0o600)
}

func (s *State) GetOrCreate(agentID, harness, id, fingerprint string) (*Conversation, error) {
	key := conversationKey(agentID, harness, id, fingerprint)
	conversation := s.Conversations[key]
	if conversation == nil && s.SchemaVersion == 1 {
		if len(s.Conversations) != 1 {
			return nil, errors.New("legacy gateway state cannot be assigned to an agent unambiguously")
		}
		legacyKey := harness + ":" + id
		legacy := s.Conversations[legacyKey]
		if legacy == nil || legacy.SourceFingerprint != fingerprint {
			return nil, errors.New("legacy gateway conversation does not match the selected agent source")
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
	c.Queue = append(c.Queue, Input{ID: id, Text: text, Status: "queued"})
	return "queued", false, nil
}

func (c *Conversation) StartNext() (Input, error) {
	if len(c.Queue) == 0 {
		return Input{}, errors.New("queue is empty")
	}
	if c.Queue[0].Status != "queued" {
		return Input{}, errors.New("next input is not queued")
	}
	c.Queue[0].Status = "active"
	return c.Queue[0], nil
}

func (c *Conversation) Complete(id, outcome string) error {
	if len(c.Queue) == 0 || c.Queue[0].ID != id || c.Queue[0].Status != "active" {
		return fmt.Errorf("active input %s does not match durable queue", id)
	}
	c.Queue = c.Queue[1:]
	c.remember(id, outcome)
	return nil
}

func (c *Conversation) RecoverUncertain() []string {
	var uncertain []string
	kept := c.Queue[:0]
	for _, input := range c.Queue {
		if input.Status == "active" {
			uncertain = append(uncertain, input.ID)
			c.remember(input.ID, "uncertain")
			continue
		}
		kept = append(kept, input)
	}
	c.Queue = kept
	return uncertain
}

func (c *Conversation) remember(id, outcome string) {
	if c.Outcomes == nil {
		c.Outcomes = map[string]string{}
	}
	if c.Outcomes[id] == "" {
		c.OutcomeOrder = append(c.OutcomeOrder, id)
	}
	c.Outcomes[id] = outcome
	for len(c.OutcomeOrder) > maxRecentOutcome {
		oldest := c.OutcomeOrder[0]
		c.OutcomeOrder = c.OutcomeOrder[1:]
		delete(c.Outcomes, oldest)
	}
}
