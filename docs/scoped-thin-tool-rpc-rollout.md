# Scoped ordinary Thin Tool RPC — local implementation / rollout draft

Status: local changes with separately authorized isolated acceptance deployments;
NOT deployed to production or approved for release. This is the authorization
prerequisite for multi-device target stability, not completed rollout acceptance.

2026-10-21 follow-up: see "Default selection and explicit task targets" below.
For native same-account turns the picker is a default. Explicit task targets
require the separately gated v2 authorization envelope and paired client below;
the legacy grant list remains selected-only.

Directory/selection follow-up: the server supports two rollout states. By
default it emits the legacy `xiaoba.runtime.v1` envelope with only routable
devices, so older XiaoBa clients keep working. Set
`CATSCO_XIAOBA_RUNTIME_V2_ENABLED=1` only after the upgraded CLI/Bot pair is
available; v2 then includes all known desktop devices, including unavailable
ones, with explicit status. v1 consumers must not be sent the v2 envelope.

For a canary rollout, leave `CATSCO_XIAOBA_RUNTIME_V2_ENABLED=0` and set
`CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS` to the comma-separated numeric UIDs of
upgraded cloud Bots. Entries must be positive decimal UIDs (for example,
`42,43`), not names, `usr`-prefixed IDs or wildcards. The global flag, when
enabled, opts in all Bots regardless of the canary list. Both production and
test Compose stacks pass these runtime flags through with disabled defaults.
Keep the server-side environment values out of source control.

The canary gate uses the recipient Bot UID when building the directory and
explicit-device capabilities, and the grant's bound AgentID at consumption.
Unlisted Bots retain v1 metadata and default-only legacy grants. Removing a Bot
from the list rejects its outstanding explicit capabilities after the new
configuration is applied; default grants keep their existing selection checks.
This setting does not enable global strict grant enforcement or broaden
delegated/cross-account access. It affects all conversations served by that Bot,
so every live body of a listed Bot must run the paired client version. Keep
configuration consistent on all server instances and restart/recreate the server
container to apply environment changes. Already dispatched work is not cancelled.

Canary acceptance sequence:

1. Deploy the upgraded cloud Bot with a known rollback release and verify its
   connection while the canary list is empty.
2. Deploy the service changes, then opt in only that Bot UID in server-side env.
   Recreate the server container and verify the effective flags and health.
3. Check one opted-in and one unlisted Bot: the former receives v2 and separate
   explicit grants; the latter stays on v1 with default-only grants.
4. Verify default A, explicit B, both devices, unchanged default and offline
   behavior using harmless files. Confirm no cross-account target is routable.
5. On rollback, remove the UID and recreate the server before restoring an old
   Bot version. Keep global v2 disabled throughout the canary period.

Username aliases use the scoped selected device; ordinary text never commits a
multi-device switch. A local WebApp confirmation UI and human-JWT-only
`GET/POST /api/conversations/device-selection` endpoint now implement the
confirmation step. Scope is actor + topic + Bot; group chats explicitly choose
a Bot. The initial GET allocates an empty revision slot, not a device or grant.
POST requires the observed nonempty revision and an owned, routable desktop.
Concurrent/stale confirmations fail with 409; there is no automatic resubmission.
Do not deploy this intermediate state as a complete multi-device experience.

Selection changes rotate a random revision. Grants capture that revision and
server lookup rechecks it, so A -> B -> A cannot resurrect A's older grants.
Automatic renewal uses compare-and-set; it cannot undo a user confirmation.
Local memory, shared memory and Redis implementations have isolated regression
coverage. Redis confirmation is transactional across nodes, not a process mutex.
Already dispatched device operations are NOT cancelled by changing selection.
The UI asks for a new instruction after confirmation; it never replays old tasks.
Confirmation is not a global execution barrier: an in-flight dispatch that has
already passed its final authorization check can race the confirmation. It keeps
its original device and must not be presented as cancelled or retargeted. A
distributed no-execution-after-confirmation guarantee is not implemented here.

Durable selection follow-up: MySQL/PostgreSQL adapters now persist actor/session
preferences in `conversation_device_selections`, with atomic revision comparison.
Production startup requires this store capability. SQL failures never fall back
to runtime preference state. Selection does not expire; execution grants still
expire after ten minutes. Existing runtime preferences, when present, migrate
with their target preserved and revision rotated. Already-lost historical intent
cannot be reconstructed; rollout must ask affected users to confirm targets.
The 30-minute runtime preference path is retained for isolated non-SQL fixtures,
not as a production durability substitute. Never downgrade a server to that path
while continuing the same device-access feature.

MySQL was verified locally on a disposable loopback-only instance: concurrent
CAS, case/account isolation, clientFoundRows compatibility, user-delete cascade,
and production-adapter readback after the entire database process restarted.
PostgreSQL 16 real-engine acceptance also passed on a separate, resource-limited
test container, reached only through a loopback SSH tunnel. It covered the adapter
contract, actual numbered up/repeated-up/down/up migration in a disposable schema,
and fresh-process adapter readback after stopping and starting the database
container. The test container was stopped afterward. Migration 000049 has NOT
been deployed to any existing application database.

Acceptance updates below cover a dispatch/switch race, baseline-source version
combinations and two physical Linux receivers. Supported release binaries,
Windows paired desktop UX/updates and broader Skill acceptance remain gates;
real-engine persistence alone does not establish production rollout readiness.

Product decision: no new per-operation approvals for Shell, scripts, deletion or
overwriting, and no changes to Skill execution workflows. Keep existing checks
and the approved WebApp device-selection UI. Existing Shell safety recognizes some
command patterns and accepts a model-supplied `confirm_dangerous` flag. That is
not a human authorization receipt; arbitrary scripts can delete or overwrite
without matching those patterns. Do not label routing/grant hardening as a
filesystem sandbox. Same-account access does not prevent mistaken deletion or
overwriting; this work guarantees scoped routing, not universal destructive-action
prevention or file-path confinement. Operation approval is not a rollout gate.

### Unknown-device intent regression (2026-10-20)

Manual cross-account testing exposed a separate intent failure: the second
account could not see/access B, but the model tried the Bot host and its own A
instead (15 calls). Scoped authorization did not retarget a B RPC; the model
changed/omitted targets itself. Not finding the file is not an acceptance pass.

The local CLI now instructs the model to stop for unknown/ambiguous/unavailable
explicit devices, removes host-fallback suggestions from target errors, and
shares a runtime-owned failure latch across a run and delegated children.
After TARGET_NOT_FOUND/AMBIGUOUS/UNAVAILABLE, later file/device tools are blocked,
including omitted targets and tool-specific shortcuts. Model JSON cannot reset
the latch. A new root run starts fresh; mid-run pending input does not reset it.
This is not natural-language intent recognition or a cancellation barrier:
initial omitted/wrong-but-authorized targets remain a model-intent risk, and
already dispatched operations cannot be recalled. Normal agent_self/Skill work
before a target failure retains existing semantics. No new approval is added.

Isolated DeepSeek Flash replay of the exact failed utterance (account uid3,
topic p2p_3_4, seq100) produced a clarification with zero tool calls. A subsequent
explicit A request (seq102) performed one successful fresh RPC to
usr3/device_8a2251bcdc5a (request 37150544-7326-4ffa-9efc-f4f365f6740c), returning
the Windows fixture. B RPC events did not increase. No chat was deleted, but
runtime history recovery loaded only two messages, not the full prior tool trace.
User manual replay also passed: seq106, run xiaoba-mvg78z3d-22420e74,
one model call and zero tool calls, independently checked against runtime logs.
This closes this specific unknown-B manual regression, not correctness for every
wording, model or Skill. Production services were not modified.

### Additional pre-release checks (2026-10-20)

CLI Skill-focused regression: 225 passed, three Windows symlink-capability
skips, no failures (evidence target-resolution-regression-jTTLl4).
Server BotDefinition Skills API contract tests also passed. The portable lab
explicitly disables SkillHub, and the model harness exposes only three base
tools; the isolated Bot's canonical definition currently has no Skills.
These checks do not replace real download/sync/activation/execution acceptance
using a separately isolated SkillHub and a nonempty test package.

Updater, Dashboard, package and build-config paths are unchanged from the CLI
checkout baseline. Packaged Windows/macOS installation remains unverified.
After a local test-tunnel interruption, the unchanged Windows Connector process
reconnected and successfully read its fixture (seq108, RPC
0cb9983c-817f-418f-ba1d-4996c5d81488). The user explicitly waived sleep/wake
testing for this work; record it as unverified, not passed, and do not request
it again or block the remaining acceptance work on it.
The lab tunnel runs on the tested Windows host: restore/check infrastructure
after wake before attributing failures to the Connector.

### Private Skill network acceptance and update fixture (2026-10-20)

A separate SkillHub source snapshot (8796c98a0c9a3d61bd93959a60262a8d6a1a7370),
loopback HTTP service, disposable PostgreSQL DB and two temporary Bots exercised
real CatsCompany Bot identity and Definition endpoints. Private upload/idempotence,
hash-checked download, other-Bot 404, clean workspace activation/SkillManager load,
actual ToolManager script execution, failed-target latch enforcement and repeat
activation passed. Evidence: skill-network-acceptance-hal77u/results.json.
The two temporary Bots and disposable DB were removed; local evidence retained.
The private package does not have public-package signature metadata: it ran via
the existing ordinary agent_self shell path, not the privileged trusted-script
path. This is not LLM Skill selection, remote-device Skill execution or signed
public-catalogue acceptance.

Unsigned Windows NSIS test versions 0.0.1/0.0.2 were built solely under
E:/XiaoBa_clone/multidevice-update-lab-20261020 with a unique appId/install GUID,
isolated state and a loopback-only generic feed; publish=never. Real packaged
startup/version detection and package SHA-512 checks passed. Updater source is
unchanged; a test-only bootstrap makes the existing button visible before login.
No install/handoff was executed automatically; user acceptance is pending.
Production signature verification and macOS remain outside this test evidence.

The first manual installer launch failed with 0xC0000005 in NSIS System.dll
at offset 0x1581, before update download/handoff. Traced installer initialization
and a guard-page reproducer localized it to electron-builder 26.8.1's
multiUser.nsh fixed-length read of a shorter SHGetKnownFolderPath allocation
(8192 UTF-16 characters). A test-only template copy now reads the actual string
length and explicitly preserves scratch registers. Neither shared node_modules,
production packaging configuration nor updater implementation was edited.
Both rebuilt isolated installers reached the directory-selection page with the
correct E: destination in 10/10 trials each; no Install button was clicked.
The launcher destination includes the product subdirectory expected by NSIS.
This fixes test preparation, not an already-published production binary.
Actual installation/download/restart acceptance is still pending; earlier
packaged-app smoke checks did not cover installer initialization.

Subsequent manual acceptance downloaded 100% and installed 0.0.2 (verified from
the installed package.json and updater source hash). Automatic relaunch did NOT
pass: no test application process remained. The test fixture explicitly used
runAfterFinish=false, hiding the assisted NSIS finish-page launch option;
non-silent --force-run does not take the silent automatic-launch branch.
Production package.json does not disable this option. This is a fixture
configuration defect, not evidence that production multi-device changes broke
relaunch. No app restart or second installation was performed during diagnosis.
Correct the fixture and use a higher test version for the remaining acceptance.

An isolated 0.0.3 fixture has now been built with runAfterFinish=true; compiler
arguments no longer define HIDE_RUN_AFTER_FINISH. Package SHA-512/size and unchanged
Electron updater source were verified, and three directory-page initialization
probes passed without installing. Installed 0.0.2 remains intact. The loopback
feed script supports 0.0.3 plus the previous 0.0.2 blockmap, and the user launcher
validates the exact old test service before restarting it and opening the existing
app. Automated service restart was blocked by the tool, so no bypass was attempted:
live 0.0.3 detection and finish-page relaunch await the user's next manual run.

Manual 0.0.2 -> 0.0.3 acceptance subsequently passed: the user confirmed automatic
opening after installation and supplied the 0.0.3 window screenshot. Installed
package.json is 0.0.3; updater logs record completed download and installer handoff,
and the installed app process was observed with --updated followed by successful
latest-version checks. Do not infer installation duration from the gap between
handoff and the user's finish-page action. Differential download fell back to a
successful full download because the prior cached installer.exe was absent; this
is NOT successful differential-update evidence. The login-page ECONNREFUSED for
127.0.0.1:19861 is an unavailable separate isolated login endpoint, not failure of
the loopback 19880 updater. Production signing, macOS installation and logged-in
 state migration across updates remain outside this evidence.

### Formal Windows packaging dependency fix (2026-10-20, local only)

The formal XiaoBa CLI checkout
`E:/XiaoBa_clone/XiaoBa-CLI-multi-device-20261019` now pins
`electron-builder` and `app-builder-lib` at 26.17.0 instead of 26.8.1.
The upstream release contains the KnownFolder `System.dll` fixed-length-read
correction identified above. `npm ci --ignore-scripts --no-audit --fund=false`,
the production build/runtime preparation steps, and
`electron-builder --config electron-builder.config.cjs --win --x64 --publish never`
all completed; the local artifact was
`release/CatsCo-1.6.1-win.exe`. A real launch of that installer on an E: test
target stayed on the directory page for about five seconds without a new
Application Error 1000 / `System.dll+0x1581`; installation was not clicked.

The same checkout passed TypeScript, release/version checks, updater
controller/error/log tests (21/21), and the previously scoped routing/
authorization/multi-device checks (79/79). The first desktop smoke attempt hit
an E:-build/C:-updater-cache cross-volume rename (`EXDEV`); placing
`TEMP`/`TMP`/`LOCALAPPDATA` on E: allowed the 0.0.1 install, update download and
file replacement to complete, with the installed package at 0.0.2. The existing
smoke fixture did not receive the post-restart 0.0.2 report, but manually
starting the replaced `CatsCoSmoke.exe --updated` succeeded. This is a fixture
reporting limitation, not evidence of NSIS crash or failed replacement.

This dependency change is now rebased onto the current `main` and submitted as
XiaoBa CLI PR #554 (`fix: upgrade electron-builder for safe Windows
installers`); the earlier conflicting PR #553 was closed. PR #554 contains only
the two dependency manifest files, preserves main's 1.6.2 and MCP SDK changes,
and has passed the repository CI and Desktop package smoke workflows, including
the Windows packaging smoke that exercises the repaired dependency path. It has
not been merged, published, or deployed. Windows production signing, macOS
installation, successful differential download and logged-in migration remain
unverified. Keep it separate from the multi-device protocol rollout. The existing CI smoke's silent
`quitAndInstall(true,true)` branch is not the non-silent finish-page branch.

## Contract

In strict rollout mode, enabled with
`CATSCO_DEVICE_GRANT_ENFORCEMENT=required`, ordinary Bot requests must include
`grant_id`, `session_key`, and `actor_user_id` alongside the existing target
owner/device and tool name. The grant is issued by CatsCompany in the current
trusted message identity; it is not a model argument and must not be printed in
prompts or logs. With the setting unset, the compatibility path still checks
owner, runtime role, liveness and capabilities but accepts an entirely legacy
unscoped request while the cloud Bot fleet is being upgraded.

The server checks the stored, active grant against the authenticated Bot/body,
actor, session, target owner/device, operation, device identity and current
capabilities. `import_file` uses the `send_file` capability. Deadlines cannot
outlive the grant. Authorization is checked again before dispatch. An absent
grant yields `device_authorization_required`; there is no unscoped fallback.

Bot ownership is not device ownership. A shared cloud Bot may operate user A's
desktop with user A's valid scoped grant, regardless of who owns the Bot. Existing
server-authorized channel identity delegation remains separate from arbitrary
cross-account targeting. SkillHub/publication requests retain their own checks.

## Compatibility matrix to verify before rollout

| Server | Cloud Bot | Expected boundary |
|---|---|---|
| Old | Old | Existing behavior; the ordinary Thin RPC authorization gap remains |
| Old | New | Client validates and sends grants; this alone does not secure the old server |
| New + compatibility defaults | Old | v1 metadata and legacy target checks keep existing clients functional |
| New + strict settings | Old | Ordinary unscoped device requests are explicitly refused; no target execution |
| New | New | Scoped requests work, invalid or expired requests are refused |

These are protocol expectations, not production or paired end-to-end acceptance
results. Desktop receivers still use the existing request/result envelope; that
compatibility must be tested with real supported receiver versions.

## Local paired contract evidence (2026-10-19)

The XiaoBa checkout provides `scripts/cross-repo-multi-device.mjs`. Set
`CATSCOMPANY_REPO` to this checkout and, if Go is not on PATH, `GO_BINARY` to its
executable, then run `node scripts/cross-repo-multi-device.mjs` from XiaoBa.
It overlays a Go test without changing this checkout and keeps test artifacts
under XiaoBa's `.audit-tmp/` (exclude from commits/releases).

Seven scenarios passed using actual server metadata and selection API, CLI
parsers/router/wire serializer and server dispatch: initial ambiguity, A and B
selection, stale A rejection, explicit refusal of legacy unscoped wire requests,
unchanged server-host local routing and offline B refusal without fallback to A.
Database/device queues and transport completion are simulated. No real model,
WebSocket receiver, installed old Bot binary or user filesystem is exercised;
this does not replace the deployment/version matrix or two-computer acceptance.

The server dispatch-boundary regression additionally verifies original-device
result attribution after switching, no retargeting of stale requests, and refusal
of B claiming A's in-flight result. This is controlled ordering, not proof of a
distributed atomic cutoff between SQL confirmation and network delivery.

## Deployment gate

### Isolated deployed-network evidence (2026-10-19)

An operator-approved, separate CatsCompany instance and PostgreSQL application
database were deployed with a newly created David test identity and two human
test accounts sharing that Bot. No production identity, model credential, chat
history or user files were copied. The application has only an internal Docker
network; a loopback SSH tunnel reaches its private container address. Production
services and Bots were not restarted or upgraded.

The XiaoBa fixture `tests/fixtures/multi-device-paired/network-runtime.ts` passed
with real pairing/enrollment, HTTP selection, WebSocket delivery, trusted message
parsers, Thin RPC sender/receiver and file tools. It verified the complete
same-account directory, A read, B-only write after switching, stale A refusal
before receiver delivery, shared-Bot cross-account refusal, explicit rejection
of legacy unscoped wire requests, and offline B refusal without falling back to A.
After stopping and restarting the entire test application and database, a second
run explicitly asserted the persisted B selection and repeated those checks.

The fixture reads test credentials from stdin, never command-line arguments.
The fixture logs the existing initial selection; persistence-specific repeat runs
provide `expectedInitialDeviceId` and assert it rather than clearing the database
or deriving the expected value from the response. The first two acceptance runs
explicitly verified fresh empty state and retained B state, respectively.
Both runs stopped the test services and tunnel afterward, retaining test data.

This uses deterministic instructions and three receivers in separate temporary
directories on one Windows host. It is NOT full model/Agent chat, two physical
computers, a load test, or a complete old/new binary compatibility matrix. Test
harness path restrictions are not a product filesystem sandbox. At this point,
full-model and physical-device acceptance were still pending; see the model
follow-up below.

### Full-model follow-up (2026-10-19)

After the deterministic probe, an operator-supplied test model key was used with
the official CatsCo relay, public model ID `deepseek-flash`, Responses mode and
`max` reasoning. A real `CatsCompanyBot` server runtime processed three test chat
turns through its normal AgentSession, provider, scoped tools and message sender:
A's random marker was read and replied, a subsequent turn after selecting B wrote
only to B, and after B disconnected the Bot reported B unavailable without
executing on A. All three checks passed (six model calls). The key was supplied
in memory/process environment, not saved in test configuration or source.

This test enabled only read/write tools with additional harness-only test-file
restrictions; it did not change product Skill workflows or safety rules. The
Bot and both device receivers ran on one Windows host against the remote isolated
service/database. It still does not cover two physical PCs or packaged desktop
login. Model configuration was provided explicitly, not through a full bound
BotDefinition login flow; startup emitted a missing-workspace-owner sync warning
while chat continued. That setup difference is not claimed as Definition/Skill
sync acceptance. All test processes, containers and tunnels were stopped after
completion. Physical-device and supported-version acceptance remain release gates.

### Physical receivers and expanded network follow-up (2026-10-20)

Seven acceptance cases passed on independent Linux test runtimes on the guga and
David hosts. The isolated David model Bot remained a separate test identity, not
the production David service. Real model turns read A, switched to B and read/
wrote only B; offline B did not fall back to A. Restart retained B, revocation
left A usable, and changing B's account kept directories and selection isolated.
Production service PID/restart snapshots were unchanged. Test processes, tokens
and tunnels were stopped/revoked; only test code and harmless fixtures remain.
This does not cover Windows desktop, tray, sleep or installer/update behavior.

An additional deterministic network run against the isolated HTTP/WebSocket/PG
service verified that an already-dispatched A request completes with A's content
after selecting B, while B cannot impersonate A's result. It also verified stale
A refusal, B-only import_file using real upload/download, and SendFileTool plus
CatsClient upload/send with a persisted attachment in the same conversation.
The channel callback is a fixture adapter, not a complete MessageSender/model
acceptance. Revoked B connector tokens cannot register again (403), retained B
grants cannot execute, and offline B never falls back to A. These receivers used
independent directories on one Windows host; do not confuse this run with the
physical Linux pair. Services and tunnels were stopped after the successful run.

### Baseline-source compatibility matrix (2026-10-20)

The baseline CLI is a4cae4b81c5c66585298dcd43507bda13d46ba6b; the baseline server
is 03c354a857fd9472d626f7f0fa36ea0886825841. Tests exercised real old/new message
parsers, CLI wire serialization, Go Hub dispatch and receiver handlers with test
storage/queues. This is not a supported-release binary or full-model matrix.

| Server | Cloud Bot | Old/new receiver outcome |
|---|---|---|
| Baseline | Baseline | Both read the test marker |
| Baseline | New | Both read the test marker |
| New | New | Both read the test marker |
| New compatibility defaults | Baseline | v1 metadata and legacy target checks preserve the old client path |
| New strict settings | Baseline | v2 metadata requires the new parser; legacy unscoped requests are denied with device_authorization_required |

Agent-host (agent_self) execution stayed local for both Bot versions with either
server. Strict enforcement is intentionally opt-in: upgrade cloud Bots and
desktop Connectors first, validate a canary, then enable both settings together.
Updating only desktop Connectors cannot fix an old Bot parser/grant limitation.

### Regression qualification (2026-10-20)

The first Windows full-suite comparison reported baseline 2457 pass / 54 fail /
30 skip and current 2516 pass / 46 fail / 30 skip. There were no current-only
failure events by relative file/test name, but this is NOT a green suite. The
archive baseline initially lacked dist and its own dependency-directory entry;
repository-nested temp directories also affect git-based fixtures. One slow
baseline worker pipeline was terminated (incomplete baseline coverage), while
the current counterpart finished normally. Targeted rechecks with build/dependency
preparation and temp directories outside the repository are recorded in the
project acceptance plan. Do not attribute the lower failure count to this change.
Windows symlink/hook limitations and missing jq must remain visible limitations,
not be hidden by disabling existing safety checks.

Targeted rechecks after preparing baseline dist/dependency layout and moving TMP
outside the repository removed all six artifact-publish failures on both sides.
The remaining 40 shared failures were 37 Windows grep beforeEach failures, two
symlink-permission failures and one missing-jq worker updater test. A baseline-only
knowledge packaging failure depended on git ls-files in the unindexed archive;
after creating a local test-only index for those resources, both versions passed
that file (5/5). These rechecks do not replace a clean full-suite pass, nor the
interrupted baseline worker pipeline. Current Electron updater controller/error/
log tests passed 21/21 and macOS artifact checks 11/11; these are automated tests,
not successful installation of an actual update on either operating system.

### Remaining release steps

2026-10-20 closeout: isolated Windows full-download fallback/install/finish-page
relaunch is accepted; keep its limitations above. Local CLI targeted routing,
authorization and updater checks passed 79/79 plus TypeScript; scoped server/store/
adapter tests and WebApp device picker 8/8 passed. This round did not reconnect
the real test databases. Production Windows packaging still uses the same
electron-builder/app-builder-lib 26.8.1 NSIS template with the reproduced
KnownFolder overread. Only the isolated fixtures include its correction.
Treat this as a separate packaging release risk, not as resolved by multi-device
acceptance. Existing CI smoke exercises silent quitAndInstall(true,true), not
the non-silent finish-page branch. No production packaging changes, PR or
deployment were performed by this audit.

1. Complete local authorization, selection and regression tests. Resolve or
   separately document platform/baseline failures; do not label a failed full
   suite green.
2. Retain the completed isolated network/physical-device evidence; extend it to
   the supported release-binary combinations and Windows UI/update acceptance.
   Broader Skill and expiry workflows are not implied by narrow tool probes.
3. Confirm cloud chat and agent-host tools still work independently of desktop
   access. Test desktop/local-Bot compatibility and Connector login/logout.
4. Inventory affected cloud Bots and explicitly coordinate their upgrades. Start
   with one approved canary; enable `CATSCO_XIAOBA_RUNTIME_V2_ENABLED=1` and
   `CATSCO_DEVICE_GRANT_ENFORCEMENT=required` together only after that canary
   passes.
5. Once strict mode is enabled, missing grants require upgrading the Bot and a
   new live user turn. Do not
   reconstruct authorization from chat history or retry without the grant.
6. Rollback must not silently restore the authorization bypass. If a canary
   fails, contain or suspend affected desktop access while investigating; any
   production rollback/containment requires operator coordination.

This draft does not authorize production deployment, remote production Bot
restarts, global strict enforcement or releases. The isolated test deployment
above was separately approved. A single dispatch/switch race does not exhaust
concurrent-turn interleavings or stress behavior; isolated real-engine and
network coverage do not constitute production rollout acceptance.

## Default selection and explicit task targets (2026-10-21)

The conversation picker selects the **default** computer. An unspecified user
desktop/file request uses that default. An explicitly requested different device
uses its own current grant; a request for multiple devices uses separate calls
and reports results per device. Task targets never change the stored default.
Device mentions, quotations and negations do not mutate selection state.

`catsco_identity.device_grants` always contains at most the selected default's
short-lived grant. With no available, active default it contains no grants.
Legacy Bots therefore never receive another device's executable grant in that
field, regardless of directory or heartbeat order. Consumers must match grants
by exact `deviceId`, never infer the default from array position.

Only with `CATSCO_XIAOBA_RUNTIME_V2_ENABLED=1`, native same-account turns also
receive `catsco_identity.device_target_authorizations`, schema
`catsco.device-targets.v1`, whose `grants` array contains independent short-lived
`targetScope: "explicit_device"` capabilities for routable devices. This is an
intentional authorization expansion in the opt-in v2 protocol, not automatic
natural-language target resolution by the server. Keep the flag off until the
paired cloud runtime has been deployed and verified; the default compatibility
state emits no such envelope. Disabling v2 also rejects previously issued
explicit grants. Merely receiving the directory does not authorize execution.

The paired runtime resolves an exact device name/ID for explicit requests and
sends `target_scope: "explicit_device"` plus that exact device ID. Both
`device_rpc` and ordinary `thin_tool_rpc` enforce the request/grant scope match;
missing/default scope cannot consume an explicit grant, and an explicit scope
cannot borrow a default grant or downgrade to unscoped compatibility. The runtime
must use a default grant for aliases such as `speaker_default`. Missing or
ambiguous targets are rejected by the paired runtime; the server enforces exact
device ID and scope rather than judging intent from natural-language text.

Every grant retains device capabilities, actor, owner, conversation, Bot, expiry
and selection revision. Switching the picker invalidates both sets of old grants.
Already-issued requests retain original target/result attribution. An offline or
expired default yields no default grant and cannot silently select online B;
only B's independent, unexpired explicit capability can operate B. Delegated
channel grants remain selected-only and receive no explicit envelope.
The picker description now says "default computer". No new operation confirmation
or Skill workflow is introduced; agent-host execution stays local. These changes
do not enable global strict-enforcement flags or constitute a production rollout.

The earlier single-selected-device descriptions in this document record the earlier
implementation/acceptance and are superseded by this section for native
same-account explicit targets.
