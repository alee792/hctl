package dispatchstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hctl/internal/interaction"
	"hctl/internal/rootfs"
)

func TestLoadMigratesLegacyStatePath(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o600)

	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetOrCreate("reviewer@legacy", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyStatePath)); !os.IsNotExist(err) {
		t.Fatalf("legacy state was not removed: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("migrated state permissions = %o", got)
	}
}

func TestLoadPrefersDispatchState(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o644)
	writeStateAt(t, root, statePath, "reviewer@dispatch", 0o600)

	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetOrCreate("reviewer@dispatch", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyStatePath)); err != nil {
		t.Fatalf("preferred dispatch state unexpectedly mutated legacy state: %v", err)
	}
}

func TestLoadRejectsBroadLegacyStatePermissions(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o644)

	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("broad legacy state permissions were not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, statePath)); !os.IsNotExist(err) {
		t.Fatalf("invalid legacy state created dispatch state: %v", err)
	}
}

func writeStateAt(t *testing.T, root, path, agentID string, mode os.FileMode) {
	t.Helper()
	conversation := &Conversation{
		ID:                "local",
		AgentID:           agentID,
		Harness:           "claude",
		SourceFingerprint: "source-1",
		Outcomes:          map[string]string{},
	}
	state := State{
		SchemaVersion: 2,
		Conversations: map[string]*Conversation{
			conversationKey(agentID, "claude", "local", "source-1"): conversation,
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(root, path, append(data, '\n'), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMigratesLegacyManifestFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".hctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "schema_version": 1,
  "conversations": {
    "claude:local": {
      "id": "local",
      "harness": "claude",
      "manifest_fingerprint": "source-1",
      "queue": [],
      "outcomes": {},
      "outcome_order": []
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(root, legacyStatePath), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Conversations["claude:local"].SourceFingerprint; got != "source-1" {
		t.Fatalf("source fingerprint = %q", got)
	}
	if _, err := state.GetOrCreate("reviewer@123", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "manifest_fingerprint") || !strings.Contains(string(data), `"schema_version": 4`) || !strings.Contains(string(data), `"agent_id": "reviewer@123"`) {
		t.Fatalf("legacy field was not migrated: %s", data)
	}
}

func TestConversationKeysSeparateAgentsAndSourceVersions(t *testing.T) {
	state := &State{SchemaVersion: 2, Conversations: map[string]*Conversation{}}
	first, err := state.GetOrCreate("reviewer@one", "claude", "local", "fingerprint-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.GetOrCreate("reviewer@two", "claude", "local", "fingerprint-1")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := state.GetOrCreate("reviewer@one", "claude", "local", "fingerprint-2")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == changed || len(state.Conversations) != 3 {
		t.Fatalf("conversations were not isolated: %#v", state.Conversations)
	}
}

func TestLoadRejectsConflictingLegacyFingerprint(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{
  "schema_version": 1,
  "conversations": {
    "claude:local": {
      "id": "local",
      "harness": "claude",
      "source_fingerprint": "source-2",
      "manifest_fingerprint": "source-1",
      "queue": [],
      "outcomes": {},
      "outcome_order": []
    }
  }
}
`)
	if err := rootfs.WriteAtomic(root, statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "fingerprints conflict") {
		t.Fatalf("conflicting fingerprints were not rejected: %v", err)
	}
}

func TestConversationWorktreeAssignmentRoundTrips(t *testing.T) {
	root := t.TempDir()
	state := &State{SchemaVersion: 2, Conversations: map[string]*Conversation{}}
	conversation, err := state.GetOrCreate("reviewer@one", "claude", "discord-one", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	conversation.WorkspaceRoot = filepath.Join(root, "worktree")
	conversation.WorktreeBranch = "hctl/reviewer/abc123"
	conversation.WorktreeRetiring = true
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Conversations[conversationKey("reviewer@one", "claude", "discord-one", "source-1")]
	if got == nil || got.WorkspaceRoot != conversation.WorkspaceRoot || got.WorktreeBranch != conversation.WorktreeBranch || !got.WorktreeRetiring {
		t.Fatalf("worktree assignment = %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "credential") {
		t.Fatalf("dispatch state contains credential material: %s", data)
	}
}

func TestInteractionLifecycleRoundTripsAfterSchemaUpgrade(t *testing.T) {
	root := t.TempDir()
	state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{}}
	conversation, err := state.GetOrCreate("reviewer@one", "claude", "discord-one", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	conversation.Interaction = validInteractionLifecycle()
	conversation.Queue = []Input{{ID: conversation.Interaction.InputID, Text: "origin", Status: "parked"}}
	conversation.InteractionTombstones = []interaction.Tombstone{{
		InteractionDigest: interaction.Digest("old-interaction"), OwnerDigest: interaction.Digest("old-owner"),
		Phase: interaction.PhaseExpired, FinishedAt: time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC),
	}}
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Conversations[conversationKey("reviewer@one", "claude", "discord-one", "source-1")]
	if got == nil || got.Interaction == nil || got.Interaction.ID != conversation.Interaction.ID || len(got.InteractionTombstones) != 1 || loaded.SchemaVersion != 4 {
		t.Fatalf("interaction state = %#v", got)
	}
}

func TestLegacySchemaUpgradesOnlyWhenWritten(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, statePath, "reviewer@legacy", 0o600)
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != 2 {
		t.Fatalf("load eagerly upgraded schema to %d", state.SchemaVersion)
	}
	before, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), `"schema_version":2`) {
		t.Fatalf("legacy bytes changed on read: %s", before)
	}
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), `"schema_version": 4`) {
		t.Fatalf("schema was not upgraded on write: %s", after)
	}
}

func TestLoadRejectsInteractionFieldsBeforeSchemaThree(t *testing.T) {
	root := t.TempDir()
	lifecycle, err := json.Marshal(validInteractionLifecycle())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema_version":2,"conversations":{"reviewer@one:source-1:claude:local":{"id":"local","agent_id":"reviewer@one","harness":"claude","source_fingerprint":"source-1","queue":[],"outcomes":{},"outcome_order":[],"interaction":` + string(lifecycle) + `}}}`)
	if err := rootfs.WriteAtomic(root, statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "requires schema version 3") {
		t.Fatalf("schema-2 interaction was accepted: %v", err)
	}
}

func TestLoadRejectsDuplicateJSONKeysAtEveryInteractionLevel(t *testing.T) {
	state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{}}
	conversation, err := state.GetOrCreate("reviewer@one", "claude", "local", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	conversation.Interaction = validInteractionLifecycle()
	conversation.Queue = []Input{{ID: conversation.Interaction.InputID, Text: "origin", Status: "parked"}}
	conversation.InteractionTombstones = []interaction.Tombstone{{
		InteractionDigest: interaction.Digest("old-interaction"),
		OwnerDigest:       interaction.Digest("old-owner"),
		Phase:             interaction.PhaseExpired,
		FinishedAt:        time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC),
	}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	surface := strings.Repeat("a", 64)
	for _, test := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "top level", old: `"schema_version":3`, new: `"schema_version":3,"schema_version":3`},
		{name: "conversation", old: `"id":"local"`, new: `"id":"local","id":"local"`},
		{name: "lifecycle", old: `"phase":"requested"`, new: `"phase":"requested","phase":"requested"`},
		{name: "request", old: `"prompt":"Proceed?"`, new: `"prompt":"Proceed?","prompt":"Proceed?"`},
		{name: "owner", old: `"surface_key":"` + surface + `"`, new: `"surface_key":"` + surface + `","surface_key":"` + surface + `"`},
		{name: "tombstone", old: `"phase":"expired"`, new: `"phase":"expired","phase":"expired"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(valid, test.old) {
				t.Fatalf("fixture does not contain %q", test.old)
			}
			root := t.TempDir()
			duplicated := strings.Replace(valid, test.old, test.new, 1)
			if err := rootfs.WriteAtomic(root, statePath, []byte(duplicated), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil || err.Error() != "dispatch state is invalid" {
				t.Fatalf("duplicate key was accepted: %v", err)
			}
		})
	}
}

func TestLoadRejectsOversizedOrConflictingInteractionTombstones(t *testing.T) {
	for _, test := range []struct {
		name       string
		tombstones []interaction.Tombstone
	}{
		{name: "too many", tombstones: interactionTombstones(interaction.MaxTerminalTombstones + 1)},
		{name: "duplicate", tombstones: append(interactionTombstones(1), interactionTombstones(1)[0])},
		{name: "pending conflict", tombstones: []interaction.Tombstone{{InteractionDigest: interaction.Digest(validInteractionLifecycle().ID), OwnerDigest: interaction.Digest("owner"), Phase: interaction.PhaseExpired, FinishedAt: time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{}}
			conversation, err := state.GetOrCreate("reviewer@one", "claude", "local", "source-1")
			if err != nil {
				t.Fatal(err)
			}
			conversation.Interaction = validInteractionLifecycle()
			conversation.Queue = []Input{{ID: conversation.Interaction.InputID, Text: "origin", Status: "parked"}}
			conversation.InteractionTombstones = test.tombstones
			if err := Save(root, state); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil {
				t.Fatal("invalid interaction tombstones were accepted")
			}
		})
	}
}

func validInteractionLifecycle() *interaction.Lifecycle {
	request := interaction.Request{
		SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Proceed?",
		Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field:  &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true},
	}
	return &interaction.Lifecycle{
		ID: "interaction_1234567890", InputID: "message-1",
		Owner:   interaction.Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request: request, Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseRequested, Delivery: interaction.DeliveryIntended,
	}
}

func TestLoadRejectsMismatchedParkedInteractionOrigins(t *testing.T) {
	for _, test := range []struct {
		name        string
		withPending bool
		queueID     string
		status      string
		wake        bool
	}{
		{name: "pending without queue", withPending: true},
		{name: "wrong origin", withPending: true, queueID: "other", status: "parked"},
		{name: "pending active", withPending: true, queueID: "message-1", status: "active"},
		{name: "parked without pending", queueID: "message-1", status: "parked"},
		{name: "wake without queue", wake: true},
		{name: "wake with active queue", queueID: "message-1", status: "active", wake: true},
		{name: "wake while pending", withPending: true, queueID: "message-1", status: "parked", wake: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{}}
			conversation, err := state.GetOrCreate("reviewer@one", "claude", "local", "source-1")
			if err != nil {
				t.Fatal(err)
			}
			if test.withPending {
				conversation.Interaction = validInteractionLifecycle()
			}
			if test.queueID != "" {
				conversation.Queue = []Input{{ID: test.queueID, Text: "origin", Status: test.status}}
			}
			conversation.InteractionWakePending = test.wake
			if err := Save(root, state); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "interaction origin") && !strings.Contains(err.Error(), "parked input") && !strings.Contains(err.Error(), "wake state") {
				t.Fatalf("invalid origin was accepted: %v", err)
			}
		})
	}
}

func TestRecoverUncertainPreservesParkedOrigin(t *testing.T) {
	conversation := &Conversation{
		Queue: []Input{
			{ID: "parked", Text: "waiting", Status: "parked"},
			{ID: "queued", Text: "later", Status: "queued"},
		},
		Outcomes: map[string]string{},
	}
	if uncertain := conversation.RecoverUncertain(); len(uncertain) != 0 {
		t.Fatalf("parked input recovered as uncertain: %v", uncertain)
	}
	if len(conversation.Queue) != 2 || conversation.Queue[0].Status != "parked" {
		t.Fatalf("parked queue changed: %#v", conversation.Queue)
	}
}

func TestOutcomeReasonPersistsWithoutChangingUncertainLifecycle(t *testing.T) {
	root := t.TempDir()
	state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{}}
	conversation, err := state.GetOrCreate("reviewer@one", "claude", "schedule", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("occurrence-1", "run"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := conversation.CompleteWithReason("occurrence-1", "uncertain", OutcomeReasonDeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	loadedConversation, err := loaded.GetOrCreate("reviewer@one", "claude", "schedule", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if loadedConversation.Outcomes["occurrence-1"] != "uncertain" || loadedConversation.OutcomeReason("occurrence-1") != OutcomeReasonDeadlineExceeded {
		t.Fatalf("loaded outcome = %#v", loadedConversation)
	}
}

func TestLoadExistingUncertainOutcomeWithoutReason(t *testing.T) {
	root := t.TempDir()
	conversation := &Conversation{
		ID: "schedule", AgentID: "reviewer@one", Harness: "claude", SourceFingerprint: "source-1",
		Outcomes: map[string]string{"occurrence-1": "uncertain"}, OutcomeOrder: []string{"occurrence-1"},
	}
	state := &State{SchemaVersion: 3, Conversations: map[string]*Conversation{
		conversationKey("reviewer@one", "claude", "schedule", "source-1"): conversation,
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(root, statePath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	loadedConversation, err := loaded.GetOrCreate("reviewer@one", "claude", "schedule", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if loadedConversation.Outcomes["occurrence-1"] != "uncertain" || loadedConversation.OutcomeReason("occurrence-1") != "" {
		t.Fatalf("legacy uncertain outcome changed: %#v", loadedConversation)
	}
	if err := Save(root, loaded); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.SchemaVersion != 4 {
		t.Fatalf("upgraded schema = %d, want 4", upgraded.SchemaVersion)
	}
}

func interactionTombstones(count int) []interaction.Tombstone {
	result := make([]interaction.Tombstone, count)
	for index := range result {
		result[index] = interaction.Tombstone{
			InteractionDigest: interaction.Digest(fmt.Sprintf("interaction-%d", index)), OwnerDigest: interaction.Digest("owner"),
			Phase: interaction.PhaseExpired, FinishedAt: time.Date(2026, 8, 7, 1, 0, index%60, 0, time.UTC),
		}
	}
	return result
}

func TestResetLifecyclePreservesWorktreeAssignment(t *testing.T) {
	conversation := &Conversation{
		SessionID: "session-1", WorkspaceRoot: "/tmp/worktree", WorktreeBranch: "hctl/test/one",
		Queue: []Input{{ID: "message-1", Status: "queued"}}, Outcomes: map[string]string{"old": "uncertain"}, OutcomeOrder: []string{"old"}, OutcomeReasons: map[string]string{"old": OutcomeReasonDeadlineExceeded},
	}
	conversation.ResetLifecycle()
	if conversation.SessionID != "" || len(conversation.Queue) != 0 || len(conversation.Outcomes) != 0 || len(conversation.OutcomeOrder) != 0 || len(conversation.OutcomeReasons) != 0 {
		t.Fatalf("lifecycle was not reset: %#v", conversation)
	}
	if conversation.WorkspaceRoot != "/tmp/worktree" || conversation.WorktreeBranch != "hctl/test/one" {
		t.Fatalf("workspace assignment was discarded: %#v", conversation)
	}
}

func TestLoadRejectsInvalidWorktreeAssignment(t *testing.T) {
	for _, fields := range []string{
		`"workspace_root":"relative","worktree_branch":"hctl/test/one",`,
		`"workspace_root":"/tmp/worktree",`,
		`"workspace_root":"/tmp/worktree","worktree_branch":"bad\\nbranch",`,
		`"worktree_retiring":true,`,
	} {
		t.Run(fields, func(t *testing.T) {
			root := t.TempDir()
			data := []byte(`{"schema_version":2,"conversations":{"key":{"id":"local","agent_id":"reviewer@one","harness":"claude","source_fingerprint":"source-1",` + fields + `"queue":[],"outcomes":{},"outcome_order":[]}}}`)
			if err := rootfs.WriteAtomic(root, statePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "conversation state is invalid") {
				t.Fatalf("invalid assignment was accepted: %v", err)
			}
		})
	}
}
