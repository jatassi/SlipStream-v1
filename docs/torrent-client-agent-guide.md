# Torrent Client Implementation — Agent Execution Guide

This document is optimized for a Claude agent executing the [torrent-client-parity-plan.md](./torrent-client-parity-plan.md). Read this file fully before beginning any implementation work.

## Context Management Strategy

**Problem:** The full plan + all reference code + all SlipStream code exceeds context. You must be strategic about what you hold in context at any time.

**Rule: Never read the entire plan into a subagent.** Subagents get only the slice of information they need.

### Orchestration Model

You (the main agent) are the **orchestrator**. You:
- Track progress through the checklist
- Dispatch subagents for individual client implementations
- Verify registrations via the verification script
- Run the linter after each phase
- Maintain coherence across the full effort

Subagents are **implementors**. They:
- Implement a single client package
- Write tests for that client
- Return the file paths they created/modified

### What to Read First (Orchestrator Warmup)

Before starting any work, read these files to build your mental model. Do this at the start of the session — do NOT delegate this to a subagent.

```
1. docs/torrent-client-parity-plan.md          — The full plan (skim phases, study your current phase)
2. internal/downloader/types/types.go           — Interface contracts (THE source of truth)
3. internal/downloader/factory.go               — Registration points
4. internal/downloader/service.go               — How clients are created and used
5. internal/downloader/client.go                — Re-exports (must stay in sync with types.go)
```

You do NOT need to read `queue.go`, `broadcaster.go`, or `completion.go` unless you are implementing Phase 0.5.

### Subagent Context Budgets

**For implementing a new client (Phase 1-3)**, each subagent needs exactly:

| File | Why | Size |
|------|-----|------|
| `internal/downloader/types/types.go` | Interface to implement | ~200 lines |
| `internal/downloader/transmission/client.go` | Reference implementation pattern | ~580 lines |
| The Sonarr reference for that specific client | API protocol details | varies |
| The client-specific section from the plan | Config fields, status mapping, endpoints | ~30 lines |

Total: ~900 lines of context. Well within budget. **Do NOT give subagents the full plan doc.**

**For Phase 0.5 (queue infrastructure)**, use a single Opus subagent (high complexity) with:

| File | Why |
|------|-----|
| `internal/downloader/service.go` | Client pool lives here |
| `internal/downloader/queue.go` | Parallel polling refactor |
| `internal/downloader/completion.go` | Parallel completion check |
| `internal/downloader/broadcaster.go` | Timeout adjustment |
| `internal/downloader/factory.go` | Client creation path |
| Phase 0.5 section from plan | Requirements |

### Subagent Prompt Template

Use this exact template when dispatching a client implementation subagent. Fill in the bracketed placeholders.

```
Implement the [CLIENT_NAME] download client for SlipStream.

## Your task
Create `internal/downloader/[PACKAGE_NAME]/client.go` that implements the `types.TorrentClient` interface.

## Interface contract (implement ALL methods)
[PASTE CONTENTS OF types.go — just the Client and TorrentClient interfaces, AddOptions, DownloadItem, TorrentInfo, Status constants, and error variables. ~100 lines.]

## Reference implementation to follow
[PASTE CONTENTS OF transmission/client.go — the full file. This is the pattern to replicate.]

## Client-specific API details
[PASTE the specific client section from the plan, e.g., "1.1 — qBittorrent"]

## Sonarr reference (for API protocol details)
[PASTE key findings from the Sonarr analysis for this specific client — endpoints, auth flow, status codes]

## Critical requirements
1. File must start with: `package [PACKAGE_NAME]`
2. Must include compile-time check: `var _ types.TorrentClient = (*Client)(nil)`
3. Must have `NewFromConfig(cfg *types.ClientConfig) *Client` constructor
4. HTTP client must have 30-second timeout (match Transmission pattern)
5. Auth session state must be stored on the Client struct and reused across calls
6. On auth failure (401, 403, expired session), re-authenticate ONCE internally then retry. Do NOT return auth errors without attempting re-auth first.
7. `List()` must return `[]types.DownloadItem` with correctly mapped Status values
8. `mapStatus()` function must cover ALL possible states from the client API — unmapped states become `types.StatusUnknown`
9. Use `context.Context` from parameters for HTTP requests (use `http.NewRequestWithContext`)
10. Progress must be 0-100 scale (convert if the client API uses 0-1 or 0.0-100.0)
11. ETA must be in seconds, -1 if unavailable
12. Size fields must be in bytes

## What NOT to do
- Do NOT add comments explaining what each method does (the interface is self-documenting)
- Do NOT add logging (the service layer handles logging)
- Do NOT create separate proxy/helper files — keep everything in client.go
- Do NOT import any packages beyond stdlib + `internal/downloader/types`
- Do NOT add methods beyond the interface + NewFromConfig + New (no legacy methods)

## Verification (MANDATORY before reporting completion)
You MUST run these commands and fix any issues before reporting your work as done. Do not report completion if any of these fail.

1. Build the project to catch compile errors and import issues:
   ```bash
   make build
   ```
   If the build fails due to the new package not being imported in factory.go yet, that's expected — instead verify just your package compiles:
   ```bash
   go build ./internal/downloader/[PACKAGE_NAME]/...
   ```

2. Run the Go linter and fix any issues in files you created:
   ```bash
   make lint
   ```
   If lint reports issues in files you did NOT create, ignore those. Only fix lint errors in your own code.

3. If both pass, report completion with the file paths you created/modified and confirmation that build and lint passed.
   If either fails, fix the issues and re-run until clean. Do NOT report completion with failing build or lint.
```

### Subagent Prompt Template — Client Test

```
Write tests for the [CLIENT_NAME] download client at `internal/downloader/[PACKAGE_NAME]/client_test.go`.

## Test pattern to follow
Use `httptest.NewServer` to mock the client's API. This is the established pattern in this codebase (see notification and metadata tests).

## Client implementation
[PASTE the client.go file that was just created]

## Required test cases

### 1. TestClient_Type — verify Type() returns correct ClientType
### 2. TestClient_Protocol — verify Protocol() returns ProtocolTorrent
### 3. TestClient_Test — mock a successful connection test response, verify no error
### 4. TestClient_Test_AuthFailure — mock a 401 response, verify ErrAuthFailed
### 5. TestClient_List — mock a response with 2-3 torrents in various states, verify:
   - Correct number of items returned
   - Status mapping is correct for each state
   - Progress is 0-100 scale
   - Size fields are in bytes
   - ETA is in seconds
### 6. TestClient_List_Empty — mock an empty response, verify empty slice (not nil)
### 7. TestClient_Add_URL — mock a successful add, verify correct ID returned
### 8. TestClient_Add_FileContent — mock a successful add with file content
### 9. TestClient_Remove — mock a successful remove
### 10. TestClient_Pause — mock a successful pause
### 11. TestClient_Resume — mock a successful resume
### 12. TestClient_GetDownloadDir — mock the config/preferences endpoint
### 13. TestClient_SessionReuse — verify that auth state persists:
   - Create client, make a List() call (auth happens)
   - Make a second List() call
   - Verify that the second call did NOT re-authenticate (use a call counter on the auth endpoint)
### 14. TestClient_SessionReauth — verify re-auth on expiry:
   - Create client, make a List() call (auth happens)
   - Server returns auth error on next List() call
   - Verify client re-authenticates and retries successfully
   - Verify the final result is correct (not an error)

## Test structure
- Use `httptest.NewServer` with `http.HandlerFunc` that switches on `r.URL.Path`
- Point client at `server.URL` (parse host/port from URL)
- For auth-based clients: track auth state in the handler (use a mutex + counter)
- Table-driven tests where multiple scenarios share the same structure

## What NOT to do
- Do NOT use testutil.NewTestDB — these are unit tests, not integration tests
- Do NOT test the factory or service layer — only test the client package
- Do NOT use external test dependencies — only stdlib + the client package

## Verification (MANDATORY before reporting completion)
You MUST run these commands and fix any issues before reporting your work as done. Do not report completion if any of these fail.

1. Run the tests you just wrote:
   ```bash
   go test -v ./internal/downloader/[PACKAGE_NAME]/...
   ```
   All tests must pass. If any fail, fix the test or the client code and re-run.

2. Run the tests with the race detector:
   ```bash
   go test -race ./internal/downloader/[PACKAGE_NAME]/...
   ```

3. Run the Go linter on your files:
   ```bash
   make lint
   ```
   Only fix lint errors in files you created/modified.

4. Build the project:
   ```bash
   make build
   ```
   If the build fails due to the new package not being imported in factory.go yet, verify just your package:
   ```bash
   go build ./internal/downloader/[PACKAGE_NAME]/...
   ```

5. Report completion only when all 4 steps pass. Include the file paths you created and confirmation that tests, race detector, lint, and build all passed.
```

## Verification Script

After implementing each client AND after modifying registration files, run:

```bash
scripts/verify-client-registration.sh [client_type]
```

This script checks all registration points for a given client type. Run it after every client implementation to catch missed registrations.

After finishing all clients in a phase, run the full check:

```bash
scripts/verify-client-registration.sh --all
```

## Phase-Specific Instructions

### Phase 0: Infrastructure Prep

**Do this yourself (orchestrator), not via subagent.** The changes are small and touch multiple files.

1. Read `internal/downloader/types/types.go` — add new `ClientType` constants
2. Read `internal/downloader/client.go` — add corresponding re-exports
3. Read `internal/database/migrations/` — find the latest migration number, create next one
4. Read `internal/downloader/service.go` — update `validClientTypes` map
5. Read `internal/downloader/factory.go` — note where new cases will go (don't add yet)
6. Run `make lint` to verify

**DB migration for type CHECK constraint:**
```bash
# Find latest migration number
ls internal/database/migrations/ | tail -5
# Create new migration with next number
```

The migration SQL should ALTER the CHECK constraint. Since SQLite doesn't support `ALTER CONSTRAINT`, you'll need to recreate the table or use a more permissive CHECK. The simplest approach: ensure the CHECK in the original migration uses an IN list that includes all types upfront, OR add a new migration that uses `CREATE TABLE IF NOT EXISTS` with the expanded type list via Goose's table recreation pattern used elsewhere in the codebase. Read 2-3 existing migrations first to understand the pattern.

### Phase 0.5: Queue Infrastructure

**Use a single Opus subagent** for this — it's the most complex phase and requires careful understanding of concurrency. The subagent must run build, tests, and lint before reporting completion (include the verification instructions below in the prompt).

Key files to modify:
- `internal/downloader/service.go` — Add `clientPool` field, modify `GetClient()`, invalidate in `Update()`/`Delete()`/`SetDB()`
- `internal/downloader/queue.go` — Refactor `GetQueue()` to use goroutines + channels
- `internal/downloader/completion.go` — Refactor `checkClientForCompletions()` and `collectActiveDownloadIDs()` for parallel polling
- `internal/downloader/broadcaster.go` — Increase outer timeout from 5s to 8s

**Write tests for this phase.** Create `internal/downloader/pool_test.go` and `internal/downloader/queue_test.go`. Test cases:
- Client pool hit (same client returned on second call)
- Client pool invalidation on Update()
- Client pool invalidation on Delete()
- Parallel GetQueue with one slow client (use a mock that sleeps)
- Parallel GetQueue with one erroring client (verify others still return)
- Cache fallback when client errors

**Verification instructions to include in the Phase 0.5 subagent prompt:**
```
## Verification (MANDATORY before reporting completion)
You MUST run ALL of these and fix any issues. Do not report completion if any fail.

1. go test -v -race ./internal/downloader/...
2. make build
3. make lint (only fix lint errors in files you modified)

Report the output of each command. If any fail, fix and re-run until clean.
```

### Phases 1-3: Client Implementations

**One subagent per client.** Use Sonnet unless noted. Dispatch using the subagent prompt template above. The templates already include mandatory build/lint/test verification steps — subagents will run these before reporting completion.

**Implementation order within a phase:**
1. Dispatch subagent to implement client.go (subagent runs `go build` + `make lint` before completing)
2. Dispatch subagent to implement client_test.go (subagent runs `go test` + `go test -race` + `make lint` + `make build` before completing; can run in parallel with step 3)
3. Register the client yourself (orchestrator):
   - Add case to `factory.go` switch in `NewClient()`
   - Add to `ImplementedClientTypes()` return list
   - Import the new package in `factory.go`
4. Run `scripts/verify-client-registration.sh [client_type]`
5. Run `make build && make lint` (verify registration changes compile and lint cleanly)
6. Run `go test ./internal/downloader/...` (full downloader test suite — catches cross-client regressions)

**Parallelism opportunity:** You can dispatch 2 client implementation subagents simultaneously if they're independent (different packages, no shared files). But do NOT dispatch more than 2 — the registration step requires sequential factory.go edits.

**For Vuze specifically:** Do NOT dispatch a subagent. Implement it yourself — it's a thin wrapper around Transmission (~100 lines) and you need Transmission's code in context anyway. Run `make build && make lint` yourself after implementing.

### Phase 4: Frontend

This is a separate effort. Read `web/CLAUDE.md` before starting. The download client form is likely in `web/src/` — search for "download client" or "downloadclient" to find the component.

## Common Pitfalls (Read Before Each Client)

### 1. Forgetting `context.Context` propagation
Every HTTP request must use `http.NewRequestWithContext(ctx, ...)`. The Transmission reference does this wrong — it uses `context.Background()` in `buildRPCRequest`. New clients should use the `ctx` parameter from the interface method. This ensures the per-client timeout from the broadcaster propagates correctly.

### 2. JSON number types in Go
`json.Unmarshal` into `interface{}` decodes all numbers as `float64`. The Transmission client handles this with `getFloat`/`getInt` helpers. New clients using typed response structs (recommended over `interface{}`) avoid this entirely. Prefer typed structs.

### 3. Progress scale mismatch
Different APIs use different scales:
- Transmission: 0.0-1.0 (multiply by 100)
- qBittorrent: 0.0-1.0 (multiply by 100)
- Deluge: 0.0-100.0 (use as-is)
- Others: varies — CHECK THE API DOCS

The `types.DownloadItem.Progress` field must be 0-100. Getting this wrong breaks the queue UI and completion detection (`progress >= 100` check in `queue.go:225`).

### 4. Empty slice vs nil
`List()` must return `[]types.DownloadItem{}` (empty slice), not `nil`. The queue code iterates the result — nil is safe but returning `nil` from a "no items" response is inconsistent with the Transmission pattern. Use `make([]types.DownloadItem, 0)`.

### 5. ETA semantics
`types.DownloadItem.ETA` is seconds remaining. -1 means unavailable. Some clients return 0 for "unknown" — map 0 to -1 if the client uses 0 to mean "infinity" or "unavailable".

### 6. The `Add()` dual-mode pattern
`Add()` must handle both `opts.URL` (magnet/HTTP link) and `opts.FileContent` (raw .torrent bytes). The Transmission client uses a switch on these. New clients must do the same. Some client APIs use separate endpoints for URL vs file upload — call the appropriate one based on which field is populated.

### 7. Download directory in DownloadItem
`DownloadItem.DownloadDir` must be the directory containing the torrent's files, not the torrent name itself. This is used by `completion.go:125` to construct `filepath.Join(d.DownloadDir, d.Name)`. Getting this wrong breaks the import pipeline.

### 8. ID field semantics
`DownloadItem.ID` is used as the key in `download_mappings` table. For most torrent clients this should be the info hash (stable across sessions). Some clients (Aria2) use a GID that's session-specific — these need special handling to store the info hash instead once metadata is resolved.

## Sonarr Reference Lookup

When implementing a client, read the Sonarr source for that client's API details. The paths are:

```
~/Developer/Sonarr/src/NzbDrone.Core/Download/Clients/{ClientDir}/
```

| Client | Sonarr Directory | Key Files to Read |
|--------|-----------------|-------------------|
| qBittorrent | `QBittorrent/` | `QBittorrent.cs`, `QBittorrentSettings.cs`, `QBittorrentProxyV2.cs` |
| Deluge | `Deluge/` | `Deluge.cs`, `DelugeSettings.cs`, `DelugeProxy.cs` |
| rTorrent | `rTorrent/` | `RTorrent.cs`, `RTorrentSettings.cs`, `RTorrentProxy.cs` |
| Vuze | `Vuze/` | `Vuze.cs`, `VuzeSettings.cs` (also read `Transmission/TransmissionBase.cs`) |
| Aria2 | `Aria2/` | `Aria2.cs`, `Aria2Settings.cs`, `Aria2Proxy.cs` |
| Flood | `Flood/` | `Flood.cs`, `FloodSettings.cs`, `FloodProxy.cs` |
| uTorrent | `uTorrent/` | `UTorrent.cs`, `UTorrentSettings.cs`, `UTorrentProxy.cs` |
| Hadouken | `Hadouken/` | `Hadouken.cs`, `HadoukenSettings.cs`, `HadoukenProxy.cs` |
| DownloadStation | `DownloadStation/` | `TorrentDownloadStation.cs`, `DownloadStationSettings.cs`, `DownloadStationProxy.cs` |
| FreeboxDownload | `FreeboxDownload/` | `TorrentFreeboxDownload.cs`, `FreeboxDownloadSettings.cs`, `FreeboxDownloadProxy.cs` |
| RQBit | `RQBit/` | `RQBit.cs`, `RQBitSettings.cs`, `RQBitProxy.cs` |
| Tribler | `Tribler/` | `TriblerDownloadClient.cs`, `TriblerDownloadSettings.cs`, `TriblerProxy.cs` |

**For subagents:** Don't give them the entire Sonarr directory. Read the Proxy file yourself (it has the actual HTTP calls) and distill the key information into the subagent prompt: endpoints, request/response formats, auth flow, status codes.

## Quality Gates

### Gate 1: Subagent self-verification (per client)

Every subagent runs build + lint + tests on its own code before reporting completion. This is enforced by the mandatory verification sections in both subagent prompt templates. **If a subagent reports completion without confirming these passed, reject the result and re-dispatch.**

### Gate 2: Orchestrator verification (per client, after registration)

After registering a client in factory.go and receiving passing results from subagents:

```bash
# Full build with the new client registered
make build

# Lint the whole project
make lint

# Run ALL downloader tests (catches cross-client regressions)
go test ./internal/downloader/...

# Verify all registration points
scripts/verify-client-registration.sh [client_type]
```

### Gate 3: Phase-level verification (after completing all clients in a phase)

```bash
# Full registration check
scripts/verify-client-registration.sh --all

# Full test suite with race detector
go test -race ./internal/downloader/...

# Full build
make build
```

If `make lint` fails on code you didn't write, ignore it. Only fix lint errors in files you touched.

## Progress Tracking

Use the TaskCreate/TaskUpdate tools to track progress. Create one task per client implementation plus one for each infrastructure phase. Mark tasks as `in_progress` when starting, `completed` only when all quality gates pass.
