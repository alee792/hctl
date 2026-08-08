# GitHub native MCP acceptance

- Automated evidence: credential-free
- Live acceptance: not executed
- Live blocker: no explicit authorization for a temporary PAT, allowlisted test
  repository, authenticated read, native approvals, or write

This record separates reproducible repository evidence from an optional live
GitHub exercise. Fake values, fake upstreams, and deterministic stdio servers
are the default. No test requires a real token, GitHub response body, model
transcript, or native session identifier.

## Credential-free evidence

The repository suite proves the following paths without rebuilding hctl or
importing GitHub code into its root Go module:

| Contract | Automated evidence |
| --- | --- |
| Official server and fixture code remain external process artifacts, with no GitHub SDK or server dependency in the hctl root binary/module | Root `go.mod`, `go list -deps ./cmd/hctl`, `TestGeneratedGitHubNativeMCPLaunchesCredentialFreeFixture` |
| Pinned Darwin/arm64 and Linux/amd64 package identities and closed `native-mcp` selection | `TestCuratedGitHubPackagePinsOfficialRelease` |
| Reviewed local package, pinned fake upstream, one-command explicit trust, and shared-cache offline reuse | `TestCuratedGitHubPackageFakeArtifactJourney`, `TestGitHubPackageMaterializerUsesPinnedFakeUpstream`, `TestGitHubPackageInstallWrapperUsesGenericTrustJourney`, `TestStorePinnedHTTPSOfflineReuse` |
| Install, inspect/status, verify, enable/disable, remove, interruption, corruption, unsupported platform, concurrency, and immutable identity drift | `TestIntegrationPackageCLIJourneyIsExplicitAndContentFree`, `TestCuratedGitHubPackageFakeFailuresAndConcurrency`, and the integration store tests |
| Exact native Claude and Codex configuration, collision rejection, startup, discovery, calls, approvals, genuinely absent vs present-empty vs invalid fake credentials, unsupported protocol/server versions, restart, and concurrent launches | `TestGitHubConnectionGeneratesExactNativeUnmanagedConfiguration`, `TestGitHubNativeMCPRejectsCollisionsBeforeMutation`, `TestGeneratedGitHubNativeMCPLaunchesCredentialFreeFixture` |
| Local and headless environment inheritance, separately bounded absent/empty/invalid fake failure categories, optional GitHub failure, normal managed-tool availability, process restart, and concurrent sessions | `TestApplySelectsInstalledGitHubNativeMCPWithoutReadingPAT`, `TestRunGitHubNativeMCPEnvironmentAndOptionalFailureForClaudeAndCodex` |
| Revalidation by hctl-owned scheduled, channel/hibernation-replacement, and writable-continuation opens after update, disablement, removal, or corruption; plain native launches remain reapply-owned | `TestScheduledOpenRevalidatesCurrentGitHubPackageState`, `TestDelayedChannelOpenAndReopenUseCurrentGitHubPackage`, `TestParkedContinuationsRevalidateCurrentGitHubPackage`, `TestWritableParkedContinuationAuditsPackageResolutionFailures` |
| Darwin-host selective staging plus Linux/amd64 GitHub-bearing direct and staged images, runtime-injection inheritance, exact pinned executable/configuration, value omission, and a GitHub-free counterpart | `TestCreateSelectivelyStagesGitHubNativeMCPClosure`, `TestGitHubImageCheckScriptParsesDirectAndStagedArguments`, and `./scripts/check-codex-image.sh` |
| Conspicuous fake value absent from source, generated files, package state/cache, staging, diagnostics, and retained output | `TestCuratedGitHubPackageFakeArtifactJourney`, `TestApplySelectsInstalledGitHubNativeMCPWithoutReadingPAT`, `TestGeneratedGitHubNativeMCPLaunchesCredentialFreeFixture`, `TestRunGitHubNativeMCPEnvironmentAndOptionalFailureForClaudeAndCodex`, `TestCreateSelectivelyStagesGitHubNativeMCPClosure` |

This coverage proves handling and non-persistence by hctl. It does not prove
secrecy from the harness, model-accessible execution tools, official server, or
other processes inheriting the environment.
The deterministic fixture's separate `missing-credential`, `empty-credential`,
and `invalid-credential` labels prove environment handling only. Production
hctl does not classify official-server or GitHub authentication failures, and
Codex's generated `env_vars` name does not synthesize an empty value when the
parent environment has no entry.

Run the focused evidence with:

```sh
go test ./internal/integration ./internal/setup ./internal/cli \
  ./internal/stage ./internal/worktree ./internal/dispatch
./scripts/check-codex-image.sh --hctl ./dist/hctl_linux_amd64
./scripts/check.sh
```

The image check requires the reproducible Linux/amd64 binary and local
prerequisites produced by its documented CI steps. Repository CI is the
retained image/staging result when those prerequisites are unavailable.

## Optional live acceptance runbook

Do not run this section merely because the automated suite passes. Obtain one
explicit authorization that names:

- the temporary fine-grained PAT or its approved secret source;
- the allowlisted test repository;
- the permitted repository permissions and expiration;
- whether only reads or one exact routine write is authorized;
- Claude, Codex, or both, and local, headless, or both; and
- the operator responsible for native trust, cleanup, and revocation.

Use an isolated runtime identity and trusted input. Then follow the
[native GitHub MCP journey](../github-native-mcp.md) and record only these
credential-free outcomes:

1. Pin hctl, `github-mcp-server`, harness, OS, and architecture versions.
2. Verify the installed package and apply offline without the PAT present.
3. Inject the temporary PAT only into the local harness launch environment.
4. Deliberately complete native project/server/tool trust and record the
   approval outcome without a native session identifier.
5. Use the discovered official tools for one allowlisted authenticated read.
6. Restart the owning headless service/container with the authorized runtime
   injection and repeat the same read.
7. If and only if separately authorized, perform the named routine write and
   verify only its bounded outcome.
8. Stop every process, remove the runtime injection, revoke the PAT, and have
   the operator verify revocation.
9. Search the allowed evidence locations for the exact temporary marker and
   retain only the pass/fail result, never matching content.

Safe retained evidence is limited to pinned versions and digests,
pseudonymous repository labels, discovered tool names, bounded timings,
approval categories, and pass/fail outcomes. Never retain a PAT, authorization
header, raw request or response body, environment dump, model transcript,
native session ID, secret-manager reference, or value-bearing command line.

Native Git and `gh` are outside this runbook unless separately authorized and
authenticated. A successful MCP write is not evidence that an exact local
branch or history was published.
