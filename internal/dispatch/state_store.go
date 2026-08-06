package dispatch

import (
	"errors"
	"sync"

	"hctl/internal/session"
)

// conversationStore owns one in-memory view of the durable dispatch state.
// A managed runtime shares it across conversation loops so saving one
// conversation cannot overwrite another conversation's concurrent progress.
type conversationStore struct {
	mu    sync.Mutex
	root  string
	state *session.State
}

type conversationRef struct {
	agentID     string
	harness     string
	id          string
	fingerprint string
}

type conversationSnapshot struct {
	sessionID string
	queueLen  int
	firstID   string
	active    bool
	exists    bool
}

func openConversationStore(root string) (*conversationStore, error) {
	state, err := session.Load(root)
	if err != nil {
		return nil, err
	}
	return &conversationStore{root: root, state: state}, nil
}

func (s *conversationStore) recover(ref conversationRef) ([]string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return nil, "", err
	}
	uncertain := conversation.RecoverUncertain()
	if len(uncertain) > 0 {
		if err := session.Save(s.root, s.state); err != nil {
			return nil, "", err
		}
	}
	return uncertain, conversation.SessionID, nil
}

func (s *conversationStore) snapshot(ref conversationRef) (conversationSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil {
		return conversationSnapshot{}, err
	}
	if conversation == nil {
		return conversationSnapshot{}, nil
	}
	snapshot := conversationSnapshot{sessionID: conversation.SessionID, queueLen: len(conversation.Queue), exists: true}
	if len(conversation.Queue) > 0 {
		snapshot.firstID = conversation.Queue[0].ID
		snapshot.active = conversation.Queue[0].Status == "active"
	}
	return snapshot, nil
}

func (s *conversationStore) lookup(ref conversationRef) (*session.Conversation, error) {
	if s.state.SchemaVersion == 1 && len(s.state.Conversations) != 1 {
		return nil, errors.New("legacy dispatch state cannot be assigned to an agent unambiguously")
	}
	for _, conversation := range s.state.Conversations {
		if conversation.Harness != ref.harness || conversation.ID != ref.id || conversation.SourceFingerprint != ref.fingerprint {
			continue
		}
		if conversation.AgentID == ref.agentID || (s.state.SchemaVersion == 1 && conversation.AgentID == "") {
			return conversation, nil
		}
	}
	return nil, nil
}

func (s *conversationStore) accept(ref conversationRef, id, text string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return "", false, err
	}
	status, duplicate, err := conversation.Accept(id, text)
	if err != nil || duplicate {
		return status, duplicate, err
	}
	if err := session.Save(s.root, s.state); err != nil {
		return "", false, err
	}
	return status, false, nil
}

func (s *conversationStore) startNext(ref conversationRef) (session.Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return session.Input{}, err
	}
	next, err := conversation.StartNext()
	if err != nil {
		return session.Input{}, err
	}
	if err := session.Save(s.root, s.state); err != nil {
		return session.Input{}, err
	}
	return next, nil
}

func (s *conversationStore) setSessionID(ref conversationRef, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return err
	}
	if conversation.SessionID == id {
		return nil
	}
	conversation.SessionID = id
	return session.Save(s.root, s.state)
}

func (s *conversationStore) complete(ref conversationRef, inputID, outcome, resultSessionID string, fresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return "", err
	}
	terminalSessionID := conversation.SessionID
	if resultSessionID != "" {
		terminalSessionID = resultSessionID
		conversation.SessionID = resultSessionID
	}
	if err := conversation.Complete(inputID, outcome); err != nil {
		return "", err
	}
	if fresh {
		conversation.SessionID = ""
	}
	if err := session.Save(s.root, s.state); err != nil {
		return "", err
	}
	return terminalSessionID, nil
}

func (s *conversationStore) reset(ref conversationRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversation(ref); err != nil {
		return err
	}
	s.state.Reset(ref.agentID, ref.harness, ref.id, ref.fingerprint)
	return session.Save(s.root, s.state)
}

func (s *conversationStore) conversation(ref conversationRef) (*session.Conversation, error) {
	return s.state.GetOrCreate(ref.agentID, ref.harness, ref.id, ref.fingerprint)
}
