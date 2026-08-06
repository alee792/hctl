package dispatch

import (
	"errors"
	"sort"
	"sync"

	"hctl/internal/session"
	"hctl/internal/worktree"
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
	workspace string
	branch    string
	retiring  bool
}

type workspaceRecord struct {
	conversation string
	fingerprint  string
	assignment   worktree.Assignment
	busy         bool
	uncertain    bool
	retiring     bool
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
	snapshot := conversationSnapshot{sessionID: conversation.SessionID, queueLen: len(conversation.Queue), exists: true, workspace: conversation.WorkspaceRoot, branch: conversation.WorktreeBranch, retiring: conversation.WorktreeRetiring}
	if len(conversation.Queue) > 0 {
		snapshot.firstID = conversation.Queue[0].ID
		snapshot.active = conversation.Queue[0].Status == "active"
	}
	return snapshot, nil
}

func (s *conversationStore) queued(ref conversationRef) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, conversation := range s.state.Conversations {
		agentMatches := conversation.AgentID == ref.agentID || s.state.SchemaVersion == 1 && len(s.state.Conversations) == 1 && conversation.AgentID == ""
		if !agentMatches || conversation.Harness != ref.harness || conversation.SourceFingerprint != ref.fingerprint {
			continue
		}
		for _, input := range conversation.Queue {
			if input.Status == "queued" {
				total++
			}
		}
	}
	return total
}

func (s *conversationStore) workspaceRecords(ref conversationRef) ([]workspaceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.SchemaVersion == 1 && len(s.state.Conversations) != 1 {
		return nil, errors.New("legacy dispatch state cannot be reconciled unambiguously")
	}
	var records []workspaceRecord
	for _, conversation := range s.state.Conversations {
		agentMatches := conversation.AgentID == ref.agentID || s.state.SchemaVersion == 1 && conversation.AgentID == ""
		if !agentMatches || conversation.Harness != ref.harness || conversation.WorkspaceRoot == "" {
			continue
		}
		uncertain := false
		for _, outcome := range conversation.Outcomes {
			if outcome == "uncertain" {
				uncertain = true
				break
			}
		}
		records = append(records, workspaceRecord{
			conversation: conversation.ID,
			fingerprint:  conversation.SourceFingerprint,
			assignment:   worktree.Assignment{Root: conversation.WorkspaceRoot, Branch: conversation.WorktreeBranch},
			busy:         len(conversation.Queue) != 0,
			uncertain:    uncertain,
			retiring:     conversation.WorktreeRetiring,
		})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].conversation < records[j].conversation })
	return records, nil
}

func (s *conversationStore) markWorkspaceRetiring(ref conversationRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil || conversation == nil {
		return err
	}
	conversation.WorktreeRetiring = true
	return session.Save(s.root, s.state)
}

func (s *conversationStore) clearRetiredWorkspace(ref conversationRef, assignment worktree.Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil || conversation == nil {
		return err
	}
	if conversation.WorkspaceRoot != assignment.Root || conversation.WorktreeBranch != assignment.Branch || !conversation.WorktreeRetiring {
		return errors.New("durable worktree retirement ownership changed")
	}
	conversation.WorkspaceRoot = ""
	conversation.WorktreeBranch = ""
	conversation.WorktreeRetiring = false
	return session.Save(s.root, s.state)
}

func (s *conversationStore) inputStatus(ref conversationRef, inputID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil || conversation == nil {
		return "", false, err
	}
	for _, input := range conversation.Queue {
		if input.ID == inputID {
			return input.Status, true, nil
		}
	}
	if outcome := conversation.Outcomes[inputID]; outcome != "" {
		return outcome, true, nil
	}
	return "", false, nil
}

func (s *conversationStore) assignWorkspaceAndAccept(ref conversationRef, workspace, branch, inputID, text string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversation(ref)
	if err != nil {
		return "", false, err
	}
	if conversation.WorkspaceRoot != "" && (conversation.WorkspaceRoot != workspace || conversation.WorktreeBranch != branch) {
		return "", false, errors.New("conversation already belongs to a different writable workspace")
	}
	priorWorkspace, priorBranch := conversation.WorkspaceRoot, conversation.WorktreeBranch
	priorQueue := append([]session.Input(nil), conversation.Queue...)
	priorOutcomes := make(map[string]string, len(conversation.Outcomes))
	for id, outcome := range conversation.Outcomes {
		priorOutcomes[id] = outcome
	}
	priorOrder := append([]string(nil), conversation.OutcomeOrder...)
	rollback := func() {
		conversation.WorkspaceRoot, conversation.WorktreeBranch = priorWorkspace, priorBranch
		conversation.Queue, conversation.Outcomes, conversation.OutcomeOrder = priorQueue, priorOutcomes, priorOrder
	}
	conversation.WorkspaceRoot = workspace
	conversation.WorktreeBranch = branch
	status, duplicate, err := conversation.Accept(inputID, text)
	if err != nil {
		rollback()
		return "", false, err
	}
	if err := session.Save(s.root, s.state); err != nil {
		rollback()
		return "", false, err
	}
	return status, duplicate, nil
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
	conversation, err := s.conversation(ref)
	if err != nil {
		return err
	}
	if conversation.WorkspaceRoot != "" {
		conversation.ResetLifecycle()
	} else {
		s.state.Reset(ref.agentID, ref.harness, ref.id, ref.fingerprint)
	}
	return session.Save(s.root, s.state)
}

func (s *conversationStore) conversation(ref conversationRef) (*session.Conversation, error) {
	return s.state.GetOrCreate(ref.agentID, ref.harness, ref.id, ref.fingerprint)
}
