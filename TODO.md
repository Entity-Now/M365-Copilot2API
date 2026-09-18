# Project TODO

Status legend: `[x]` verified, `[ ]` not verified, `[-]` blocked, `[c]` canceled by user and not verified.

## Repository Baseline

- [x] Read global `AGENTS.md`.
- [x] Confirm project-level `AGENTS.md` was absent, then add release and protocol guardrails for this workstream.
- [x] Inspect `git status`, `git diff`, and `git log --oneline -10` without altering existing work.
- [x] Reconciled the current dirty baseline without reset, checkout, clean, deletion, or overwriting existing and unknown changes.

## Graph Authorization Wizard

- [-] Verify authorization start, status, revoke, batch user creation, error handling, and localization end to end. Deterministic handler coverage exists, but the Graph routes remain intentionally unregistered and real tenant end-to-end verification is blocked by missing credentials and production authorization; no real authorization result is claimed.
- [x] Verify newly created accounts are not represented as OAuth-authorized accounts. Added `TestGraphBatchUsersDoesNotCreateOAuthAuthorizedAccounts`; it provisions through a deterministic local Graph stub, verifies the OAuth account store remains empty, and passed 20 consecutive runs with `GOROOT=D:\Go` and a 5-minute timeout in 1.789s.

## Network Fault Matrix

- [x] Register `/responses` as a behavior-identical alias of `/v1/responses`; verify it uses API-key authentication, never falls into administrator middleware, and does not return `administrator login required`. The targeted test passed 20 consecutive runs with a 5-minute timeout.
- [x] Fix WebSocket test sequencing so business frames are channel-controlled and the server maintains a read loop.
- [x] Run the corrected WebSocket close/frame-channel test 20 consecutive times with an explicit timeout.
- [x] Run the current targeted network, half-open, overload, and isolation tests 20 consecutive times with an explicit timeout. Latest rerun passed: `internal/chathub` 4.704s and `internal/web` 92.261s.
- [x] Obtain a clean 100-run concurrent stress result. The failure was caused by test design exhausting Windows ephemeral ports, not production behavior: the HTTP performance test created excessive connections across repeated process-local servers. Its transport now explicitly reuses and caps 32 connections. The latest 100-run targeted stress rerun passed with a 10-minute timeout: `internal/chathub` 21.133s, `internal/outbound` 5.271s, and `internal/web` 18.893s, without weakening assertions.
- [ ] Verify WebSocket reuse, slow consumers, cancellation propagation, server close, frame-channel close, backpressure, and goroutine steady state. Corrected frame-channel close test passed 20 consecutive runs; connection-pool, transport, cancellation, protocol, session, tool, overload, and isolation-targeted tests passed 100 consecutive runs with a 10-minute timeout. ConnPool close is now idempotent and its close/slow-consumer tests passed 100 consecutive runs. Connection-pool lifecycle and active WebSocket goroutine steady-state tests each passed 20 consecutive runs with a 2-minute timeout; remaining matrix items are not yet fully verified.
- [ ] Verify slow write and real WebSocket slow-consumer behavior. TCP reset and slow read passed 20 consecutive targeted runs; half-open connection, EOF, dial failure, TLS handshake failure, timeout, connection-pool exhaustion, idle close, and recovery also passed 20 consecutive targeted runs, all with explicit timeouts.
- [x] Verify HTTP/1.1 connection reuse and HTTP/2 multiplexing with measurable connection counts; passed 20 consecutive targeted runs.

## Isolation

- [ ] Verify isolation between clients sharing one API key across concurrent requests, sessions, conversation IDs, response frames, tool state, and buffers. Tenant/session response namespaces, request-local session headers, and request-local function/custom tool output call IDs passed 20 consecutive runs and 100 concurrent-stress repetitions; full response-frame, conversation, and buffer isolation remains pending.
- [ ] Verify Chat Completions and Responses protocol state cannot cross request or client boundaries.

## Circuit Breaking And Overload

- [x] Verify half-open admits one concurrent probe in the current targeted test suite.
- [x] Verify a successful probe restores availability in the current targeted test suite.
- [x] Verify a repeated 429 re-enters cooldown in the current targeted test suite.
- [ ] Verify repeated 429 preserves the existing configured cooldown duration in all paths.
- [x] Verify non-idempotent POST requests are not replayed in the current targeted test suite.
- [x] Verify local concurrency overload returns 503 rather than an upstream-style 429 in the current targeted test suite.

## Protocol And Product Coverage

- [x] Verify Chat Completions tool calls, tool results, usage, streaming, cancellation, and error mapping. The targeted protocol, MCP, tool, call ID, streaming, usage, cancellation, error, conversation, and isolation suite passed 20 consecutive runs in 6.041s.
- [x] Verify Responses tool calls, tool results, usage, streaming, cancellation, and error mapping. The same targeted suite passed 20 consecutive runs; the expected inner-request failure path emitted `response.created` followed by `response.failed`, never `response.completed`.
- [ ] Verify WebUI network errors, recovery messages, accessibility, and i18n. The two WebUI copies are byte-identical (SHA-256 `15135CB6CF1B2B5EB0AE8F6276E69E7F17B5EB100A1556BB93BFBD55809D574A`), but end-to-end UX, accessibility, and localization remain unverified.
- [c] Codex、OpenCode、Claude Code 三客户端完整复杂任务按用户要求取消；不执行、不作为发布阻塞，且不得标记为已验证完成。

## Performance Evidence

- [ ] Measure throughput, P50, P95, P99, failure recovery, maximum concurrency, allocations, and goroutine steady state. The WebSocket pool-hit performance test now uses 100 requests per run to avoid exhausting Windows ephemeral ports and passed 100 consecutive runs in 6.416s. Its metric is explicitly named `pool_hit_rate` because each request still creates, takes, and closes a freshly warmed connection; it does not prove cross-request connection reuse.
- [-] Cross-request WebSocket reuse is not applicable to continued conversations. `internal/chathub/client.go:392-397` permits a pre-warmed connection only for a fresh request without conversation or session IDs, while `internal/chathub/connpool_reuse_test.go:20-27` records that continuing an upstream conversation requires a fresh WebSocket to avoid an immediate empty completion. Do not weaken this protocol boundary to improve reuse metrics.
- [ ] Record the complete reproducible baseline and any after-change comparison. Latest five-run microbenchmarks: `BenchmarkParseRetryAfterSeconds` 10.74-11.25 ns/op, 0 B/op, 0 allocs/op; `BenchmarkAccountHealthTryAcquire` 41.32-41.68 ns/op, 0 B/op, 0 allocs/op; `BenchmarkServerConcurrencyLimit` 3455-3669 ns/op, 5376-5377 B/op, 15 allocs/op. The end-to-end loopback HTTP concurrency benchmark measured 92847-96791 ns/op, 6335-6396 B/op, and 73 allocs/op across five runs (Windows amd64, Intel Xeon E3-1226 v3). The corrected HTTP latency and concurrency test passed 20 consecutive runs with 1000 requests and concurrency 32; observed samples include throughput 4708.70-5726.81 req/s, P50 1.9710-2.4764 ms, P95 15.9998-20.7473 ms, P99 18.9591-25.1274 ms, and 32 simultaneously active handlers. The renamed `BenchmarkConnPoolWebSocketPoolHit` measured 1446279 ns/op, 38878 B/op, and 153 allocs/op over 100 iterations. A representative pool-hit run measured 669.86 req/s, P50 1.3522ms, P95 4.8807ms, P99 62.7633ms, maximum active 2, and pool hit rate 100%; the test passed 100 consecutive runs in 6.452s. This is pool-hit evidence only; completed requests close their WebSockets, and continued conversations intentionally require fresh connections.
- [ ] Make only a minimal optimization if measurements identify a bottleneck.
- [x] Do not introduce Radix Tree, HTTP/3, a custom HTTP stack, `sync.Pool`, `unsafe`, or sensitive-slice pooling without evidence.

## Responses Protocol Compatibility

- [ ] P0 trace Responses request conversion and upstream event sources without altering existing or unknown work.
- [ ] P0 support `instructions`, `max_output_tokens`, `parallel_tool_calls`, `tool_choice`, `reasoning`, `include`, `temperature`, `text`, `service_tier`, `context_management`, and `previous_response_id`; reject unsafe unsupported parameters with `unsupported_parameter`.
- [ ] P0 preserve per-turn highest-priority instructions without inheriting stale instructions through `previous_response_id`.
- [ ] P0 fix `function_call_output`/`call_id`, `response.failed`, premature stream disconnects, UTF-8 chunk corruption, path/link damage, and deterministic dual-source event deduplication/completion.
- [-] P0 native ChatHub client-tool state machine is blocked by missing HAR evidence for stable call IDs, argument deltas, result continuation, and completion association. Synthetic API-plugin and MCPServer forwarding are disabled instead of inferred.
- [ ] P0 implement a tool-state ledger for in-progress, awaiting-result, completed, and final-response states; prevent fabricated completion and premature summaries.

## Administration And Security

- [ ] P1 implement API-key-to-account bindings, persistence, scheduling filters, failover, WebUI management, and unbound-key healthy-account load balancing.
- [ ] P1 expose existing settings safely with encrypted persistence, masked secrets, administrator authorization, and CSRF protection.
- [ ] P1 complete batch Microsoft 365 user UX, encrypted initial-password storage, administrator-only view/export, secure clearing, auditing, license availability display, and actionable master-key initialization.
- [ ] P1 synchronize `web/index.html` and `internal/web/web/index.html` without overwriting unknown changes.
- [ ] P1 complete usage passthrough and dashboard accounting semantics.

## Required Regression Coverage

- [ ] Add tests for request semantics, unsupported parameters, per-turn instructions, previous response handling, dual-source tool events, UTF-8 chunks, paths/links, call IDs, failures, and tool-loop completion.
- [ ] Add tests for API-key account bindings, settings persistence, password encryption, authorization, CSRF, and byte-identical frontend copies.

## Quality Gates

- [x] Run `gofmt` on changed Go files, including the added connection-pool steady-state test.
- [x] Rerun `go test ./... -count=1 -timeout=10m` after the final test edits. Latest full rerun passed with `GOROOT=D:\Go`; `internal/auth` 0.751s, `internal/chathub` 1.285s, `internal/mcp` 1.258s, `internal/outbound` 0.919s, and `internal/web` 6.994s.
- [x] Rerun `go vet ./...` after fixing the Windows ephemeral-port stress-test design.
- [x] Rerun `go build ./...` after fixing the Windows ephemeral-port stress-test design.
- [x] Rerun `git diff --check` after fixing the Windows ephemeral-port stress-test design. Latest rerun passed with line-ending conversion warnings only; `web/index.html` and `internal/web/web/index.html` were byte-identical.
- [c] Race 检测按用户要求取消；不执行、不作为发布阻塞，且不得标记为已验证完成或已通过。
- [-] Complete independent security, concurrency-risk, protocol, cross-request leakage, denial-of-service, and authorization-boundary review. Independent read-only Agent A is unavailable in the current tool environment, so no independent review conclusion is claimed.
- [-] Complete independent performance-evidence, over-design, test-realism, WebUI UX, and i18n review. Independent read-only Agent B is unavailable in the current tool environment, so no independent review conclusion is claimed.
- [ ] Fix every Critical and High review finding, then rerun every gate.
- [ ] Run targeted tests at least 20 consecutive times with `GOROOT=D:\Go` and the matching Go `PATH`.
- [ ] Run `go test ./... -count=1 -timeout=10m`, `go vet ./...`, `go build ./...`, and `git diff --check` after final edits.
- [ ] Rebuild and restart only the development service on port 4242; verify the home page and critical APIs without touching port 4141.
- [ ] Report root causes, changed files with line numbers, test evidence, unsupported parameters, and manual verification steps without committing or publishing.

## Version And Release Gate

- [ ] Verify version metadata and release prerequisites.
- [c] Codex、OpenCode、Claude Code 三客户端复杂任务按用户要求取消；不执行、不作为发布阻塞，且不得标记为已验证完成。
- [-] Graph 真实租户授权仍受外部凭据阻塞，因为缺少 `M365_GRAPH_CLIENT_SECRET` 和 `M365_GRAPH_TENANT_ID`；不得宣称真实租户授权已通过。
- [ ] Keep release blocked until every required gate and real-client check has evidence.
- [x] Do not deploy, commit, push, tag, release, touch `D:\M365-Copilot2API`, use port 4141, or change backend cooldown duration.

## Zero-Downtime Production Updates

## Current Requested Workstream

- [x] Read global instructions, existing TODO, repository status, current diff, recent commits, and open pull requests/issues without altering unknown work.
- [-] Load `m365-experiment-release`; the skill is not installed or discoverable in the permitted environment.
- [x] Perform read-only structured mining of all 16 HAR files under `D:\Users\Downloads`; no HAR data was uploaded externally. The evidence contains snapshot, delta, final-result, and completion frames but no complete native client-tool lifecycle or stable upstream call ID.
- [x] Add project `AGENTS.md` release guardrails and `docs/har-mining/10-tool-protocol-status.md`, separating observed facts, implementation inferences, and unverified tool capabilities without including credentials.
- [x] Document and retain tool priority: client user tools first, third-party tools second, official cloud tools only as fallback.
- [ ] Preserve system/developer instructions without overwrite, stripping, or demotion; add `system_directive_followed=false` regression coverage.
- [ ] Fix local-file and multimodal handling without assuming `/mnt/data` or a Linux-only sandbox.
- [ ] Fix #78 conversation forgetting, cross-session leakage, conversation switching, and cache isolation.
- [ ] Fix #77 Responses streaming `call_id`, `name`, and `function_call_output` protocol association.
- [ ] Fix duplicate tool call ID `bash:0`; keep tool IDs unique across turns and protocol associations stable.
- [ ] Re-test #75 and #79 throttle/cooldown classification without changing existing cooldown durations prematurely.
- [ ] Verify the current official GPT-5 tokenizer/encoding guidance online and record primary sources before implementation.
- [ ] Start token estimation only after the model's first output byte; use a bounded low-priority queue, batch persistence, low-resource behavior, and non-blocking queue overflow semantics.
- [ ] Clearly label locally estimated usage and never present it as exact upstream billing or metering.
- [ ] Optimize reasoning/think event output while preserving protocol behavior.
- [ ] Implement and test Windows headless startup using Task Scheduler or Windows Service, including boot startup, crash recovery, working directory/environment inheritance, log rotation, duplicate-instance prevention, uninstall, and verification.
- [ ] Implement one-time silent stable-release checks after login, explicit user approval, SHA-256 and architecture verification, isolated updater, candidate health checks, rollback, and preserved old binaries/configuration/data.
- [ ] Implement true zero-downtime switching through a stable proxy and blue/green instances; retain the old instance until candidate health succeeds and in-flight requests drain.
- [ ] Rehearse update, rollback, and switching on random ports and temporary directories before any production operation.
- [ ] Baseline the network path before modification and inject TCP reset, half-open, DNS, TLS, HTTP/2, slow-client, pool exhaustion, cancellation, backpressure, and non-idempotent replay faults.
- [ ] Measure P50/P95/P99 latency, throughput, allocations, and goroutine steady state; do not introduce HTTP/3, a custom HTTP stack, unsafe code, or sensitive-data pooling without evidence.
- [ ] Re-test every currently open pull request and issue relevant to this repository and record evidence individually.
- [ ] Add unit, handler, end-to-end, concurrency, and security regression tests.
- [ ] Run targeted tests 20 consecutive times and concurrency stress 100 consecutive times using a strategy that avoids ephemeral-port exhaustion.
- [ ] Set `GOROOT=D:\Go` and PATH, then run `gofmt`, `go test ./... -count=1 -timeout=10m`, `go vet ./...`, `go build ./...`, and `git diff --check`.
- [ ] Verify both `index.html`/`login.html` copies are byte-identical and contain no U+FFFD replacement character.
- [ ] Before production switching, run independent Agent A security/protocol/leakage/DoS review and Agent B performance/UX/i18n/test/update-experience review; fix all Critical/High findings and rerun gates.
- [ ] Preserve all existing and unknown modifications; never use reset, checkout, clean, deletion, or destructive overwrite.
- [ ] Do not commit, push, tag, create a pull request, or publish a release without further explicit authorization in this turn.
- [ ] Update production only after all gates pass; preserve one-command rollback and restore immediately on any failed health check.
- [ ] Produce final evidence covering TODO status, HAR discoveries and documents, root causes, changed files with exact lines, test matrix, performance measurements, Windows startup verification, update/rollback rehearsal, production continuity, residual risks, and final git status.

- [ ] Introduce a stable local listener or reverse proxy on `127.0.0.1:4141`; application instances must use separate blue and green candidate ports.
- [ ] Preserve WebSocket upgrades, SSE streaming, request bodies, forwarding headers, timeouts, and connection-close semantics through the proxy.
- [ ] Define readiness, liveness, version, account-pool, and critical API checks that automation can call without exposing administrator credentials.
- [ ] Start the candidate with an explicit Windows service definition containing the executable path, working directory, arguments, environment, log destinations, and restart policy.
- [ ] Verify the candidate version, configuration, account count, upstream connectivity, and critical API behavior before routing production traffic to it.
- [ ] Make upstream switching atomic and retain the previous upstream as the immediate rollback target.
- [ ] Stop assigning new requests to the old instance, then drain HTTP requests, SSE streams, WebSockets, and other long-running connections under separately defined deadlines.
- [ ] Define WebSocket and SSE behavior during deployment, including maximum connection age, client reconnect guidance, and forced closure only after the drain deadline.
- [ ] Audit in-memory session and conversation state; move required cross-instance state to a shared store or document which sessions must reconnect during a switch.
- [ ] Audit every shared configuration and data file for concurrent-read, concurrent-write, advisory-lock, atomic-write, and corruption behavior before allowing blue and green instances to share it.
- [ ] Give each instance separate temporary files, caches, PID files, and logs; keep account, key, Graph, quota, and cooldown settings unchanged.
- [ ] Add deterministic port allocation, process ownership, stale-process detection, PID recording, and prevention of two instances binding the same candidate port.
- [ ] Implement rollback when readiness fails, version differs, account loading changes unexpectedly, critical APIs fail, or post-switch error and latency thresholds regress.
- [ ] Record release SHA-256, PE machine type, old and new versions and PIDs, switch timestamp, drain duration, health evidence, and rollback outcome without recording secrets.
- [ ] Package the proxy and both application slots as Windows services with dependency ordering, restricted service identities, recovery policy, and controlled log rotation.
- [ ] Constrain automatic updates to non-draft, non-prerelease releases with an exact tag, official `checksums.txt`, matching SHA-256, valid PE/AMD64 headers, staging-only extraction, and preserved rollback binaries.
- [ ] Reject updates when the startup environment, working directory, command line, health authorization, data-lock behavior, or rollback path cannot be reproduced exactly.
- [ ] Build an integration test covering healthy switch, failed candidate, post-switch regression, rollback, WebSocket/SSE drain, long requests, locked data files, and machine restart.
- [ ] Roll out in phases: observable manual blue-green operation, scripted switch and rollback, Windows service hardening, then narrowly scoped automatic updates after repeated production-like validation.

## Authorized Workstream Scope

- [x] Limit work to `D:\M365-Copilot2API-dev`, local HAR files under `D:\Users\Downloads`, and the explicitly authorized production directory `D:\M365-Copilot2API`.
- [x] Preserve all existing and unknown changes; never use reset, checkout, clean, deletion, or destructive overwrite.
- [x] Inspect repository status, complete diff, this TODO, recent commits, production processes and listeners, scheduled tasks/services/startup state, open GitHub issues and pull requests, and the local HAR inventory.
- [x] Confirm production PID 16316 serves `127.0.0.1:4141` from `D:\M365-Copilot2API\m365-copilot2api.exe`; no matching scheduled task, service, or startup entry was reported.
- [x] Inventory 16 local HAR files; analyze locally only, redact secrets, and never upload HAR content.
- [x] Keep this TODO current after every completed verification or discovered blocker.

## Parallel Workstreams

- [x] Add a bounded, explicit-timeout OpenCode two-model test orchestrator: AI A generates a credential-free deterministic complex task and AI B executes and verifies it while production, destructive Git operations, and port 4141 remain prohibited. execution remains dependent on locally configured OpenCode providers and credentials.
- [ ] Agent A: read-only HAR analysis covering hidden MCP behavior, tool declarations, instruction envelopes, files, multimodal requests, conversations, reasoning, usage, rate limits, and undocumented endpoints.
- [ ] Agent B: fix #73, system/developer instruction fidelity, local files, multimodal handling, #77, duplicate `bash:0`, conversation switching, cache isolation, and stable cross-turn tool IDs.
- [ ] Agent C: verify official GPT-5 tokenizer guidance and implement bounded low-priority asynchronous token estimation that begins only after the first response byte, batches storage, and degrades without blocking when full.
- [ ] Agent D: implement free local hidden Windows startup with recovery, exact working directory and environment inheritance, singleton protection, log rotation, uninstall entry point, and tests.
- [ ] Agent E: implement stable-release detection, SHA-256 and architecture checks, independent updater, candidate health checks, proxy or dual-instance switching, draining, and rollback.
- [ ] Agent F: establish network fault injection, performance baselines, and security review before making minimal evidence-based changes.
- [-] Independent sub-agent execution is blocked if the current tool environment exposes no Agent/Task tool; keep workstreams non-overlapping when performed directly.

## Protocol And Tool Correctness

- [x] Expose a conservative per-account capability profile through the administrator-protected read-only accounts endpoint. Capabilities are derived only from local account state and observed metering; unverified image capability remains explicitly false.
- [x] Enforce and document tool priority in the validated router prompt: user/client tools first, third-party tools second, official cloud tools only as fallback.
- [ ] Preserve system and developer instructions without overwrite, stripping, reclassification, or downgrade.
- [ ] Add regressions for `system_directive_followed=false` and system/developer instruction fidelity.
- [x] Add focused `system_directive_followed=false` content-policy detection coverage, including case-insensitive failure signals and a non-rejection check for `system_directive_followed=true`; `internal/chathub` passed once and the focused tests passed 20 consecutive runs. Full system/developer instruction fidelity remains unverified.
- [ ] Remove false claims that clients can access only `/mnt/data` or a Linux sandbox.
- [ ] Restore local-file and multimodal recognition with handler-level and end-to-end coverage.
- [ ] Fix #78 conversation forgetting, switching, cross-talk, and cache namespace isolation.
- [x] Verify #77 Responses streaming `response.output_item.added` includes non-empty `call_id` and `name` before argument deltas; added a focused regression test and passed it 20 consecutive times. `function_call_output` correlation remains covered separately by the existing Responses history validation.
- [x] Fix `duplicate tool call id: bash:0`; tool call IDs are UUID-based and a focused uniqueness regression passed 20 consecutive runs with 1,000 repeated calls per run.
- [ ] Optimize reasoning/think handling without changing instruction priority or stream ordering.

## Quota And Token Accounting

- [ ] Reproduce and classify #75/#79 rate limits and cooldowns without changing existing cooldown durations.
- [ ] Distinguish quota exhaustion, transient throttling, image quota, account-wide cooldown, and retryable transport failures.
- [ ] Verify official GPT-5 tokenizer/encoding recommendations from primary sources.
- [ ] Start local token estimation only after the model emits its first byte; never block first-byte latency or streaming.
- [ ] Use a bounded queue, low-resource workers, batching, queue-full nonblocking degradation, and graceful shutdown.
- [ ] Store estimates server-side and label them explicitly as local estimates, not upstream billing truth.

## Windows Startup And Recovery

- [ ] Select Task Scheduler or Windows Service based on recovery, privilege, environment, and operability evidence.
- [ ] Run at boot without a console window using only free local components.
- [ ] Preserve exact executable path, working directory, environment, configuration, and data locations.
- [ ] Add singleton protection, stale-PID handling, crash recovery, bounded restart policy, and observable health state.
- [ ] Add size/time-based log rotation with retention and no secret leakage.
- [ ] Provide idempotent install, status, start, stop, and uninstall entry points plus automated tests.

## Update And Rollback

- [ ] Detect only stable non-draft, non-prerelease releases and verify exact tag, SHA-256, PE architecture, and expected files.
- [ ] Run updates through an independent updater in a temporary directory and random candidate port.
- [ ] Preserve old binary, configuration, data, logs, and a one-command rollback path.
- [ ] Health-check the candidate before routing any production traffic; immediately retain or restore the old version on failure.
- [ ] Implement genuine zero-downtime switching with a front proxy or dual-instance routing, never single-EXE port takeover on 4141.
- [ ] Stop new traffic to the old instance and drain HTTP, SSE, WebSocket, and long-running requests under documented deadlines.
- [ ] Exercise successful update, failed candidate, failed switch, post-switch regression, restart, and rollback scenarios.

## Network Performance And Security

- [ ] Establish immutable pre-change latency, throughput, allocation, goroutine, connection, and error baselines.
- [ ] Cover TCP reset, half-open connections, DNS failure, TLS failure, HTTP/2 behavior, slow clients, pool exhaustion, cancellation propagation, and backpressure.
- [ ] Verify non-idempotent requests are never replayed automatically.
- [ ] Record P50/P95/P99, throughput, allocations, goroutine steady state, and resource ceilings.
- [ ] Make only minimal evidence-supported changes; do not introduce HTTP/3, custom HTTP stacks, `unsafe`, or sensitive-data buffer pools without evidence.
- [ ] Complete protocol, authentication, authorization, injection, secret-leakage, path/file, cross-session leakage, and denial-of-service review.

## Issues Pull Requests And HAR Documentation

- [ ] Reproduce and evaluate every open issue: #80, #79, #78, #77, #76, #75, #73, #72, #64, #61, #60, #51, #45, and #42.
- [ ] Reproduce and evaluate every open pull request: #74, #71, #69, #68, #67, #63, #62, and #58.
- [x] Document the tool-protocol HAR boundary and release decision under `docs/har-mining/10-tool-protocol-status.md`.
- [x] Separate observed HAR evidence, implementation inferences, and unverified capabilities in the tool-protocol report.

## Verification Gates

- [x] Run the focused account capability, batch scheduling, and token-estimator tests 20 consecutive times with `GOROOT=D:\Go`; `internal/web` passed in 4.734s.
- [x] Improve mobile navigation, touch targets, settings controls, tables, cards, and modal overflow while keeping both `index.html` copies byte-identical.
- [x] Set `GOROOT=D:\Go` and prepend `D:\Go\bin` to `PATH` for every completed Go gate.
- [x] Run `gofmt` on changed Go files.
- [x] Run targeted image API and cache tests 20 consecutive times; `internal/web` passed in 1.870s.
- [x] Run concurrency stress 100 consecutive times using a bounded 32-connection loopback HTTP transport that avoids ephemeral-port exhaustion; `TestServerConcurrencyLimitHTTPPerformance` performed 1000 operations per run and passed 100 consecutive runs in 8.973s.
- [x] Run `go test ./... -count=1 -timeout=10m`; all packages passed.
- [x] Run `go vet ./...`; passed.
- [x] Run `go build ./...`; passed.
- [x] Run `git diff --check`; passed with line-ending conversion warnings only.
- [x] Verify both copies of `index.html` and `login.html` are byte-identical and contain no U+FFFD replacement character.
- [ ] Perform a fresh independent security/protocol/cross-session-leakage/DoS review before production switching.
- [ ] Perform a second fresh independent performance-evidence/UX/i18n/test-integrity/update-experience review before production switching.
- [ ] Fix every Critical and High finding and rerun all affected gates.
- [ ] Verify production continuity during candidate startup, traffic switch, drain, rollback, and machine restart.
- [ ] Record final TODO state, workstream results, HAR findings and document paths, root causes, changed files with exact lines, test matrix, performance evidence, startup verification, update/rollback rehearsal, production continuity, residual risks, and final git status.
- [ ] Commit, push, tag, create the v0.7.0 Release, and update production only after every required gate and both independent reviews pass; stop immediately on any gate, CI, asset, health, continuity, or rollback-check failure.
- [ ] Keep `scripts/provision-accounts.ps1` untracked and exclude scripts, secrets, logs, build artifacts, and runtime data from staging.
