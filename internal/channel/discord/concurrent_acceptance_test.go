package discord

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channelconfig"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/project"
	"hctl/internal/session"
	"hctl/internal/worktree"
)

type acceptanceDelivery struct {
	channel   string
	content   string
	reference string
}

func TestConcurrentDiscordMutationsUseRealHarnessAdaptersAndWorktrees(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			control := t.TempDir()
			t.Setenv("FAKE_CONTROL_DIR", control)
			executable := writeAcceptanceExecutable(t, harnessName)
			repo := discordAcceptanceProject(t)
			p, err := project.Load(repo, harnessName)
			if err != nil {
				t.Fatal(err)
			}
			var driver harness.Driver
			if harnessName == "claude" {
				driver = claude.New(executable)
			} else {
				driver = codex.New(executable)
			}
			config := Config{
				Token: "test.token.value", Executable: "/usr/bin/true", Audit: io.Discard,
				TurnTimeout: 30 * time.Second, IdleTimeout: time.Hour, MaxResident: 2, MaxActive: 2,
				Runtime: channelconfig.Profile{
					ApplicationID: "application", BotUserID: "bot", AllowedUserID: "person",
					AllowedGuildID: "guild", AllowedChannelID: "guild-channel",
				},
			}
			runtime, err := New(p, driver, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runtime.Close)
			runtime.typing = func(string) error { return nil }
			deliveries := make(chan acceptanceDelivery, 2)
			runtime.deliver = func(channel string, message *discordgo.MessageSend) error {
				reference := ""
				if message.Reference != nil {
					reference = message.Reference.MessageID
				}
				deliveries <- acceptanceDelivery{channel: channel, content: message.Content, reference: reference}
				return nil
			}

			runtime.handleMessage(nil, acceptanceMessage("guild-message", "guild-channel", "guild"))
			runtime.handleMessage(nil, acceptanceMessage("dm-message", "dm-channel", ""))

			ready := waitAcceptanceProcesses(t, control, 2)
			state, err := session.Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			guild := acceptanceConversation(state, conversationID("application", "guild-channel"))
			dm := acceptanceConversation(state, conversationID("application", "dm-channel"))
			if guild == nil || dm == nil || guild.WorkspaceRoot == "" || dm.WorkspaceRoot == "" || guild.WorkspaceRoot == dm.WorkspaceRoot || guild.WorktreeBranch == dm.WorktreeBranch || guild.SessionID == dm.SessionID {
				t.Fatalf("isolated durable assignments: guild=%#v dm=%#v", guild, dm)
			}
			if _, err := os.Stat(filepath.Join(guild.WorkspaceRoot, "mutation.txt")); err != nil {
				t.Fatalf("guild mutation missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dm.WorkspaceRoot, "mutation.txt")); err != nil {
				t.Fatalf("DM mutation missing: %v", err)
			}

			dmProcess := ready[dm.WorkspaceRoot]
			guildProcess := ready[guild.WorkspaceRoot]
			if dmProcess == "" || guildProcess == "" || dmProcess == guildProcess {
				t.Fatalf("ready worktree processes = %#v", ready)
			}
			releaseAcceptanceProcess(t, control, dmProcess)
			first := waitAcceptanceDelivery(t, deliveries)
			if first.channel != "dm-channel" || first.reference != "dm-message" || first.content != "done" {
				t.Fatalf("first out-of-order delivery = %#v", first)
			}
			releaseAcceptanceProcess(t, control, guildProcess)
			second := waitAcceptanceDelivery(t, deliveries)
			if second.channel != "guild-channel" || second.reference != "guild-message" || second.content != "done" {
				t.Fatalf("second out-of-order delivery = %#v", second)
			}

			// A restart must preserve both dirty worktrees and explain that local
			// decision without exposing it through Discord status.
			runtime.Close()
			var audit bytes.Buffer
			config.Audit = &audit
			if harnessName == "claude" {
				driver = claude.New(executable)
			} else {
				driver = codex.New(executable)
			}
			restarted, err := New(p, driver, config)
			if err != nil {
				t.Fatal(err)
			}
			restarted.Close()
			if strings.Count(audit.String(), "dirty or untracked work") != 2 {
				t.Fatalf("restart reconciliation diagnostics:\n%s", audit.String())
			}
			restartedState, err := session.Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			restartedGuild := acceptanceConversation(restartedState, conversationID("application", "guild-channel"))
			restartedDM := acceptanceConversation(restartedState, conversationID("application", "dm-channel"))
			if restartedGuild == nil || restartedDM == nil || restartedGuild.WorkspaceRoot != guild.WorkspaceRoot || restartedDM.WorkspaceRoot != dm.WorkspaceRoot {
				t.Fatalf("restart lost multi-surface worktrees: guild=%#v dm=%#v", restartedGuild, restartedDM)
			}
		})
	}
}

func TestDiscordStartupRetiresOnlyProvenCleanMergedWorktree(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			executable := writeAcceptanceExecutable(t, harnessName)
			repo := discordAcceptanceProject(t)
			p, err := project.Load(repo, harnessName)
			if err != nil {
				t.Fatal(err)
			}
			workspaceManager, err := worktree.New(context.Background(), p, "/usr/bin/true")
			if err != nil {
				t.Fatal(err)
			}
			conversation := conversationID("application", "guild-channel")
			_, assignment, err := workspaceManager.Provision(context.Background(), conversation)
			if err != nil {
				t.Fatal(err)
			}
			state, err := session.Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			durable, err := state.GetOrCreate(p.AgentID, harnessName, conversation, p.SourceFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			durable.WorkspaceRoot = assignment.Root
			durable.WorktreeBranch = assignment.Branch
			if err := session.Save(repo, state); err != nil {
				t.Fatal(err)
			}
			var driver harness.Driver
			if harnessName == "claude" {
				driver = claude.New(executable)
			} else {
				driver = codex.New(executable)
			}
			var audit bytes.Buffer
			runtime, err := New(p, driver, Config{
				Token: "test.token.value", Executable: "/usr/bin/true", Audit: &audit,
				Runtime: channelconfig.Profile{ApplicationID: "application", BotUserID: "bot", AllowedUserID: "person", AllowedGuildID: "guild", AllowedChannelID: "guild-channel"},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.Close()
			if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
				t.Fatalf("clean merged worktree remains: %v", err)
			}
			persisted, err := session.Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			got := acceptanceConversation(persisted, conversation)
			if got == nil || got.WorkspaceRoot != "" || got.WorktreeBranch != "" || got.WorktreeRetiring {
				t.Fatalf("retired durable state = %#v", got)
			}
			if !strings.Contains(audit.String(), "retired after verifying it was inactive, clean, and merged") {
				t.Fatalf("retirement diagnostic = %q", audit.String())
			}
		})
	}
}

func acceptanceMessage(id, channel, guild string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: id, ChannelID: channel, GuildID: guild, Content: "make a change",
		Author: &discordgo.User{ID: "person"}, Mentions: []*discordgo.User{{ID: "bot"}},
	}}
}

func acceptanceConversation(state *session.State, id string) *session.Conversation {
	for _, conversation := range state.Conversations {
		if conversation.ID == id {
			return conversation
		}
	}
	return nil
}

func waitAcceptanceProcesses(t *testing.T, directory string, count int) map[string]string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		ready := map[string]string{}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "ready-") {
				continue
			}
			root, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				continue
			}
			ready[strings.TrimSpace(string(root))] = strings.TrimPrefix(entry.Name(), "ready-")
		}
		if len(ready) == count {
			return ready
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d writable harness processes", count)
	return nil
}

func releaseAcceptanceProcess(t *testing.T, directory, process string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "release-"+process), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitAcceptanceDelivery(t *testing.T, deliveries <-chan acceptanceDelivery) acceptanceDelivery {
	t.Helper()
	select {
	case delivery := <-deliveries:
		return delivery
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for Discord delivery")
		return acceptanceDelivery{}
	}
}

func discordAcceptanceProject(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"instructions.md":     "---\ndescription: acceptance agent\n---\nHelp with repository changes.\n",
		"channels/discord.md": "---\nmode: ambient\n---\nRespond to direct requests.\n",
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.email", "hctl@example.invalid"}, {"config", "user.name", "hctl acceptance"}, {"add", "."}, {"commit", "--quiet", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return repo
}

func writeAcceptanceExecutable(t *testing.T, harnessName string) string {
	t.Helper()
	content := claudeAcceptanceScript
	if harnessName == "codex" {
		content = codexAcceptanceScript
	}
	path := filepath.Join(t.TempDir(), "fake-"+harnessName)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const claudeAcceptanceScript = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
if [ "${1-}" = "--permission-mode" ]; then
  echo "--permission-mode ${2-}"
  exit 0
fi
session_id="claude-$$"
previous=""
for argument in "$@"; do
  if [ "$previous" = "--resume" ]; then session_id="$argument"; fi
  previous="$argument"
done
while IFS= read -r line; do
  printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
  if [ "$HCTL_EXECUTION_POLICY" = "read-only" ]; then
    text="HCTL_REQUEST_WRITE_ACCESS"
  else
    printf 'mutation\n' > mutation.txt
    pwd > "$FAKE_CONTROL_DIR/ready-$$"
    while [ ! -f "$FAKE_CONTROL_DIR/release-$$" ]; do sleep 0.01; done
    text="done"
  fi
  printf '{"type":"stream_event","session_id":"%s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"%s"}}}\n' "$session_id" "$text"
  printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"%s"}\n' "$session_id" "$text"
done
`

const codexAcceptanceScript = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "codex-cli 0.144.1"
  exit 0
fi
thread_id="codex-$$"
turn=0
while IFS= read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}\n' "$id"
      ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      resumed=$(printf '%s\n' "$line" | sed -n 's/.*"threadId":"\([^"]*\)".*/\1/p')
      if [ -n "$resumed" ]; then thread_id="$resumed"; fi
      if [ "$HCTL_EXECUTION_POLICY" = "workspace-write" ]; then sandbox="workspaceWrite"; else sandbox="readOnly"; fi
      printf '{"id":%s,"result":{"thread":{"id":"%s"},"sandbox":{"type":"%s","networkAccess":false},"approvalPolicy":"never"}}\n' "$id" "$thread_id" "$sandbox"
      ;;
    *'"method":"turn/start"'*)
      turn=$((turn + 1))
      turn_id="turn-$$-$turn"
      printf '{"id":%s,"result":{"turn":{"id":"%s","items":[],"status":"inProgress"}}}\n' "$id" "$turn_id"
      if [ "$HCTL_EXECUTION_POLICY" = "read-only" ]; then
        text="HCTL_REQUEST_WRITE_ACCESS"
      else
        printf 'mutation\n' > mutation.txt
        pwd > "$FAKE_CONTROL_DIR/ready-$$"
        while [ ! -f "$FAKE_CONTROL_DIR/release-$$" ]; do sleep 0.01; done
        text="done"
      fi
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"%s","turnId":"%s","itemId":"output","delta":"%s"}}\n' "$thread_id" "$turn_id" "$text"
      printf '{"method":"turn/completed","params":{"threadId":"%s","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$thread_id" "$turn_id"
      ;;
  esac
done
`
