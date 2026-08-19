// Package channelconfig owns the three control-result vocabulary strings that
// core's generated harness instructions (internal/setup) emit verbatim into
// CLAUDE.md/AGENTS.md for Discord-enabled agents. Core never interprets these
// strings: it only writes them into instructions text so the model can
// produce them. The channel runtime (internal/channel/controller) is the sole
// interpreter, matching agent output against these exact values to suppress a
// reply or trigger a write-access continuation. See decision D-6 in
// docs/workbench/channel-seam-audit.md.
package channelconfig

const (
	NoReplyResult            = "HCTL_NO_REPLY"
	RequestWriteAccessResult = "HCTL_REQUEST_WRITE_ACCESS"
	WriteContinuationPrompt  = "Write access is now available in this conversation's isolated worktree. Continue the original user request now."
)
