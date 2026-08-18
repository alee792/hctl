package dispatch

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hctl/internal/dispatchstate"
	"hctl/internal/interaction"
	"hctl/internal/worktree"
)

var interactionOutcomePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// conversationStore owns one in-memory view of the durable dispatch state.
// A managed runtime shares it across conversation loops so saving one
// conversation cannot overwrite another conversation's concurrent progress.
type conversationStore struct {
	mu    sync.Mutex
	root  string
	state *dispatchstate.State
	save  func(string, *dispatchstate.State) error
}

type conversationRef struct {
	agentID     string
	harness     string
	id          string
	fingerprint string
}

type conversationSnapshot struct {
	sessionID       string
	queueLen        int
	firstID         string
	active          bool
	waitingForInput bool
	exists          bool
	workspace       string
	branch          string
	retiring        bool
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
	state, err := dispatchstate.Load(root)
	if err != nil {
		return nil, err
	}
	return &conversationStore{root: root, state: state, save: dispatchstate.Save}, nil
}

func (s *conversationStore) persist() error { return s.save(s.root, s.state) }

func (s *conversationStore) recover(ref conversationRef) ([]string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var uncertain []string
	var sessionID string
	err := s.persistMutationIfChanged(func() (bool, error) {
		conversation, err := s.lookup(ref)
		if err != nil {
			return false, err
		}
		if conversation == nil {
			return false, nil
		}
		sessionID = conversation.SessionID
		uncertain = conversation.RecoverUncertain()
		return len(uncertain) > 0, nil
	})
	return uncertain, sessionID, err
}

func (s *conversationStore) recoverTask(ref conversationRef) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var recovered []string
	err := s.persistMutationIfChanged(func() (bool, error) {
		conversation, err := s.lookup(ref)
		if err != nil || conversation == nil || len(conversation.Queue) == 0 {
			return false, err
		}
		recovered, err = conversation.RecoverTaskUncertain()
		return len(recovered) > 0, err
	})
	return recovered, err
}

func (s *conversationStore) terminalizeTask(ref conversationRef, inputID, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
		conversation, err := s.conversation(ref)
		if err != nil {
			return err
		}
		if len(conversation.Queue) == 0 || conversation.Queue[0].ID != inputID {
			return errors.New("task input does not match durable queue")
		}
		if conversation.Queue[0].Status == "queued" {
			if _, err := conversation.StartNext(); err != nil {
				return err
			}
		}
		if err := conversation.Complete(inputID, outcome); err != nil {
			return err
		}
		conversation.SessionID = ""
		return nil
	})
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
	snapshot := conversationSnapshot{sessionID: conversation.SessionID, queueLen: len(conversation.Queue), waitingForInput: conversation.Interaction != nil, exists: true, workspace: conversation.WorkspaceRoot, branch: conversation.WorktreeBranch, retiring: conversation.WorktreeRetiring}
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

func (s *conversationStore) runnable(ref conversationRef) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conversations []string
	for _, conversation := range s.state.Conversations {
		if conversation.AgentID != ref.agentID || conversation.Harness != ref.harness || conversation.SourceFingerprint != ref.fingerprint || !conversation.InteractionWakePending || conversation.Interaction != nil || len(conversation.Queue) == 0 || conversation.Queue[0].Status != "queued" {
			continue
		}
		conversations = append(conversations, conversation.ID)
	}
	sort.Strings(conversations)
	return conversations
}

func (s *conversationStore) interactionConversations(ref conversationRef) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conversations []string
	for _, conversation := range s.state.Conversations {
		if conversation.AgentID == ref.agentID && conversation.Harness == ref.harness && conversation.SourceFingerprint == ref.fingerprint && conversation.Interaction != nil {
			conversations = append(conversations, conversation.ID)
		}
	}
	sort.Strings(conversations)
	return conversations
}

// recoverInteractionContinuations distinguishes answered work that is safe to
// claim once from a continuation whose external effect may already have begun.
// The latter becomes uncertain and is never scheduled automatically.
func (s *conversationStore) recoverInteractionContinuations(ref conversationRef) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var resumable []string
	err := s.persistMutationIfChanged(func() (bool, error) {
		changed := false
		for _, conversation := range s.state.Conversations {
			if conversation.AgentID != ref.agentID || conversation.Harness != ref.harness || conversation.SourceFingerprint != ref.fingerprint || conversation.Interaction == nil {
				continue
			}
			pending := conversation.Interaction
			switch {
			case pending.Phase == interaction.PhaseAnswered && pending.Resume == interaction.ResumePending:
				resumable = append(resumable, conversation.ID)
			case pending.Phase == interaction.PhaseResuming && pending.Resume == interaction.ResumeIntended:
				pending.Resume = interaction.ResumeUncertain
				changed = true
			}
		}
		sort.Strings(resumable)
		return changed, nil
	})
	return resumable, err
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
		if pending := conversation.Interaction; pending != nil && pending.Phase == interaction.PhaseResuming && pending.Resume == interaction.ResumeUncertain {
			uncertain = true
		}
		records = append(records, workspaceRecord{
			conversation: conversation.ID,
			fingerprint:  conversation.SourceFingerprint,
			assignment:   worktree.Assignment{Root: conversation.WorkspaceRoot, Branch: conversation.WorktreeBranch},
			busy:         len(conversation.Queue) != 0 || conversation.Interaction != nil,
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
	return s.persistMutation(func() error {
		conversation, err := s.lookup(ref)
		if err != nil || conversation == nil {
			return err
		}
		conversation.WorktreeRetiring = true
		return nil
	})
}

func (s *conversationStore) clearRetiredWorkspace(ref conversationRef, assignment worktree.Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
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
		return nil
	})
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

func (s *conversationStore) outcomeReason(ref conversationRef, inputID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil || conversation == nil {
		return "", err
	}
	return conversation.OutcomeReason(inputID), nil
}

func (s *conversationStore) assignWorkspaceAndAccept(ref conversationRef, workspace, branch, inputID, text string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil {
		return "", false, err
	}
	if conversation != nil {
		if conversation.WorkspaceRoot != "" && (conversation.WorkspaceRoot != workspace || conversation.WorktreeBranch != branch) {
			return "", false, errors.New("conversation already belongs to a different writable workspace")
		}
		if status, duplicate, blocked := conversationAdmissionStatus(conversation, inputID); duplicate || blocked {
			return status, duplicate, nil
		}
	}
	var status string
	err = s.persistMutation(func() error {
		conversation, err = s.conversation(ref)
		if err != nil {
			return err
		}
		if conversation.WorkspaceRoot != "" && (conversation.WorkspaceRoot != workspace || conversation.WorktreeBranch != branch) {
			return errors.New("conversation already belongs to a different writable workspace")
		}
		conversation.WorkspaceRoot = workspace
		conversation.WorktreeBranch = branch
		status, _, err = conversation.Accept(inputID, text)
		return err
	})
	return status, false, err
}

func (s *conversationStore) lookup(ref conversationRef) (*dispatchstate.Conversation, error) {
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
	conversation, err := s.lookup(ref)
	if err != nil {
		return "", false, err
	}
	if conversation != nil {
		if status, duplicate, blocked := conversationAdmissionStatus(conversation, id); duplicate || blocked {
			return status, duplicate, nil
		}
	}
	var status string
	err = s.persistMutation(func() error {
		conversation, err = s.conversation(ref)
		if err != nil {
			return err
		}
		status, _, err = conversation.Accept(id, text)
		return err
	})
	return status, false, err
}

func (s *conversationStore) startNext(ref conversationRef) (dispatchstate.Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next dispatchstate.Input
	err := s.persistMutation(func() error {
		conversation, err := s.conversation(ref)
		if err != nil {
			return err
		}
		if conversation.Interaction != nil {
			return ErrWaitingForInput
		}
		next, err = conversation.StartNext()
		if err == nil {
			conversation.InteractionWakePending = false
		}
		return err
	})
	return next, err
}

func (s *conversationStore) setSessionID(ref conversationRef, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutationIfChanged(func() (bool, error) {
		conversation, err := s.conversation(ref)
		if err != nil {
			return false, err
		}
		if conversation.SessionID == id {
			return false, nil
		}
		conversation.SessionID = id
		return true, nil
	})
}

func (s *conversationStore) complete(ref conversationRef, inputID, outcome, resultSessionID string, fresh bool) (string, error) {
	return s.completeWithReason(ref, inputID, outcome, "", resultSessionID, fresh)
}

func (s *conversationStore) completeWithReason(ref conversationRef, inputID, outcome, reason, resultSessionID string, fresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var terminalSessionID string
	err := s.persistMutation(func() error {
		conversation, err := s.conversation(ref)
		if err != nil {
			return err
		}
		terminalSessionID = conversation.SessionID
		if resultSessionID != "" {
			terminalSessionID = resultSessionID
			conversation.SessionID = resultSessionID
		}
		if err := conversation.CompleteWithReason(inputID, outcome, reason); err != nil {
			return err
		}
		if fresh {
			conversation.SessionID = ""
		}
		return nil
	})
	return terminalSessionID, err
}

func (s *conversationStore) reset(ref conversationRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
		conversation, err := s.conversation(ref)
		if err != nil {
			return err
		}
		if conversation.Interaction != nil {
			return ErrConversationBusy
		}
		if conversation.WorkspaceRoot != "" {
			conversation.ResetLifecycle()
		} else {
			s.state.Reset(ref.agentID, ref.harness, ref.id, ref.fingerprint)
		}
		return nil
	})
}

func (s *conversationStore) loadInteraction(ref conversationRef) (interaction.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.lookup(ref)
	if err != nil || conversation == nil {
		return interaction.DurableState{}, err
	}
	return cloneInteractionState(interaction.DurableState{Pending: conversation.Interaction, Tombstones: conversation.InteractionTombstones})
}

func (s *conversationStore) openInteraction(ref conversationRef, pending *interaction.Lifecycle) error {
	if pending == nil {
		return errors.New("interaction is required")
	}
	copy, err := cloneInteractionState(interaction.DurableState{Pending: pending})
	if err != nil {
		return err
	}
	if copy.Pending.Phase != interaction.PhaseRequested || copy.Pending.Delivery != interaction.DeliveryPending {
		return errors.New("new interaction must have pending delivery")
	}
	if err := copy.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
		conversation, err := s.conversation(ref)
		if err != nil {
			return err
		}
		if conversation.Interaction != nil {
			return interaction.ErrAlreadyPending
		}
		if err := (interaction.DurableState{Pending: copy.Pending, Tombstones: conversation.InteractionTombstones}).Validate(); err != nil {
			return err
		}
		if err := conversation.Park(copy.Pending.InputID); err != nil {
			return err
		}
		conversation.Interaction = copy.Pending
		return nil
	})
}

func (s *conversationStore) updateInteraction(ref conversationRef, id string, mutate func(*interaction.Lifecycle) error) error {
	if mutate == nil {
		return errors.New("interaction mutation is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
		conversation, err := s.lookup(ref)
		if err != nil {
			return err
		}
		if conversation == nil || conversation.Interaction == nil || conversation.Interaction.ID != id {
			return interaction.ErrInteractionMissing
		}
		current, err := cloneInteractionState(interaction.DurableState{Pending: conversation.Interaction})
		if err != nil {
			return err
		}
		originID, inputID := current.Pending.ID, current.Pending.InputID
		if err := mutate(current.Pending); err != nil {
			return err
		}
		if current.Pending == nil || current.Pending.ID != originID || current.Pending.InputID != inputID {
			return errors.New("interaction correlation cannot change")
		}
		if err := current.Pending.Validate(); err != nil {
			return err
		}
		conversation.Interaction = current.Pending
		return nil
	})
}

func (s *conversationStore) finishInteraction(ref conversationRef, finish interaction.FinishRequest) error {
	if finish.InteractionID == "" || finish.FinishedAt.IsZero() || finish.FinishedAt.Location() != time.UTC || finish.FinishedAt.Nanosecond() != 0 {
		return errors.New("interaction finish request is invalid")
	}
	if finish.Phase != interaction.PhaseCompleted && finish.Phase != interaction.PhaseCancelled && finish.Phase != interaction.PhaseExpired {
		return errors.New("interaction finish phase is invalid")
	}
	if finish.OriginOutcome != "" && !interactionOutcomePattern.MatchString(finish.OriginOutcome) {
		return errors.New("interaction origin outcome is invalid")
	}
	if len(finish.ResultSessionID) > 512 || !utf8.ValidString(finish.ResultSessionID) || strings.IndexFunc(finish.ResultSessionID, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("interaction result session is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMutation(func() error {
		conversation, err := s.lookup(ref)
		if err != nil {
			return err
		}
		if conversation == nil {
			return interaction.ErrInteractionMissing
		}
		if conversation.Interaction == nil || conversation.Interaction.ID != finish.InteractionID {
			digest := interaction.Digest(finish.InteractionID)
			for _, tombstone := range conversation.InteractionTombstones {
				answerMatches := finish.AnswerDigest == "" || finish.AnswerDigest == tombstone.AnswerDigest
				if tombstone.InteractionDigest == digest && tombstone.Phase == finish.Phase && answerMatches {
					return nil
				}
			}
			return interaction.ErrInteractionMissing
		}
		pending := conversation.Interaction
		if finish.AnswerDigest != "" && finish.AnswerDigest != pending.AnswerDigest {
			return errors.New("interaction finish answer digest conflicts with the accepted answer")
		}
		if finish.ResultSessionID != "" && finish.Phase != interaction.PhaseCompleted {
			return errors.New("only a completed interaction may update the result session")
		}
		switch finish.Phase {
		case interaction.PhaseCompleted:
			normal := pending.Phase == interaction.PhaseResuming && pending.Resume == interaction.ResumeIntended && !finish.Recovery
			recovered := pending.Phase == interaction.PhaseResuming && (pending.Resume == interaction.ResumeFailed || pending.Resume == interaction.ResumeUncertain) && finish.Recovery
			if !normal && !recovered {
				return interaction.ErrInteractionLate
			}
		case interaction.PhaseExpired:
			if pending.Phase != interaction.PhaseRequested && pending.Phase != interaction.PhaseRendered || finish.FinishedAt.Before(pending.ExpiresAt) {
				return interaction.ErrInteractionLate
			}
		case interaction.PhaseCancelled:
			renderFailed := pending.Phase == interaction.PhaseRequested && pending.Delivery == interaction.DeliveryIntended
			answerCancelled := pending.Phase == interaction.PhaseAnswered && pending.Answer != nil && pending.Answer.Action == interaction.ActionCancel
			resumeRecovered := pending.Phase == interaction.PhaseResuming && (pending.Resume == interaction.ResumeFailed || pending.Resume == interaction.ResumeUncertain) && finish.Recovery
			if !renderFailed && !answerCancelled && !resumeRecovered {
				return interaction.ErrInteractionLate
			}
		}
		answerDigest := finish.AnswerDigest
		if answerDigest == "" {
			answerDigest = pending.AnswerDigest
		}
		outcome := finish.OriginOutcome
		if outcome == "" {
			outcome = string(finish.Phase)
		}
		if finish.ResultSessionID != "" {
			conversation.SessionID = finish.ResultSessionID
		}
		if err := conversation.CompleteParked(pending.InputID, outcome); err != nil {
			return err
		}
		conversation.InteractionWakePending = len(conversation.Queue) > 0
		conversation.InteractionTombstones = append(conversation.InteractionTombstones, interaction.Tombstone{
			InteractionDigest: interaction.Digest(pending.ID),
			OwnerDigest:       interaction.Digest(pending.Owner.SurfaceKey + ":" + pending.Owner.PrincipalKey),
			AnswerDigest:      answerDigest,
			Phase:             finish.Phase,
			FinishedAt:        finish.FinishedAt,
		})
		for len(conversation.InteractionTombstones) > interaction.MaxTerminalTombstones {
			conversation.InteractionTombstones = conversation.InteractionTombstones[1:]
		}
		conversation.Interaction = nil
		return (interaction.DurableState{Tombstones: conversation.InteractionTombstones}).Validate()
	})
}

type boundInteractionStore struct {
	store *conversationStore
	ref   conversationRef
	wake  func() error
}

func (s *conversationStore) interactionStore(ref conversationRef) interaction.Store {
	return &boundInteractionStore{store: s, ref: ref}
}

func (s *conversationStore) interactionStoreWithWake(ref conversationRef, wake func() error) interaction.Store {
	return &boundInteractionStore{store: s, ref: ref, wake: wake}
}

func (s *boundInteractionStore) Load() (interaction.DurableState, error) {
	return s.store.loadInteraction(s.ref)
}

func (s *boundInteractionStore) Open(pending *interaction.Lifecycle) error {
	return s.store.openInteraction(s.ref, pending)
}

func (s *boundInteractionStore) Update(id string, mutate func(*interaction.Lifecycle) error) error {
	return s.store.updateInteraction(s.ref, id, mutate)
}

func (s *boundInteractionStore) Finish(finish interaction.FinishRequest) error {
	if err := s.store.finishInteraction(s.ref, finish); err != nil {
		return err
	}
	if s.wake != nil {
		return s.wake()
	}
	return nil
}

func (s *conversationStore) persistMutation(mutate func() error) error {
	prior, err := cloneSessionState(s.state)
	if err != nil {
		return err
	}
	if err := mutate(); err != nil {
		s.state = prior
		return err
	}
	if err := s.persist(); err != nil {
		s.state = prior
		return err
	}
	return nil
}

func (s *conversationStore) persistMutationIfChanged(mutate func() (bool, error)) error {
	prior, err := cloneSessionState(s.state)
	if err != nil {
		return err
	}
	changed, err := mutate()
	if err != nil {
		s.state = prior
		return err
	}
	if !changed {
		return nil
	}
	if err := s.persist(); err != nil {
		s.state = prior
		return err
	}
	return nil
}

func conversationInputStatus(conversation *dispatchstate.Conversation, inputID string) (string, bool) {
	for _, input := range conversation.Queue {
		if input.ID == inputID {
			return input.Status, true
		}
	}
	if status := conversation.Outcomes[inputID]; status != "" {
		return status, true
	}
	return "", false
}

func conversationAdmissionStatus(conversation *dispatchstate.Conversation, inputID string) (status string, duplicate, blocked bool) {
	if status, duplicate = conversationInputStatus(conversation, inputID); duplicate {
		return status, true, false
	}
	if conversation.Interaction != nil {
		return string(LifecycleWaiting), false, true
	}
	return "", false, false
}

func cloneSessionState(state *dispatchstate.State) (*dispatchstate.State, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.New("cannot snapshot dispatch state")
	}
	var clone dispatchstate.State
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, errors.New("cannot snapshot dispatch state")
	}
	return &clone, nil
}

func cloneInteractionState(state interaction.DurableState) (interaction.DurableState, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return interaction.DurableState{}, errors.New("cannot snapshot interaction state")
	}
	var clone interaction.DurableState
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return interaction.DurableState{}, errors.New("cannot snapshot interaction state")
	}
	return clone, nil
}

func (s *conversationStore) conversation(ref conversationRef) (*dispatchstate.Conversation, error) {
	return s.state.GetOrCreate(ref.agentID, ref.harness, ref.id, ref.fingerprint)
}
