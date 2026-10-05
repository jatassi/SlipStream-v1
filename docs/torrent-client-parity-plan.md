# Torrent Client Parity Plan

Achieve parity with Sonarr's torrent client support in SlipStream.

## Agent Execution Resources

| Resource | Path | Purpose |
|----------|------|---------|
| Agent Guide | `docs/torrent-client-agent-guide.md` | Context management, subagent prompts, pitfalls, phase instructions |
| Verification Script | `scripts/verify-client-registration.sh` | Checks all registration points per client |
| Test Template | `internal/downloader/client_test_template.go.example` | httptest-based test scaffold for new clients |
| Sonarr Source | `~/Developer/Sonarr/src/NzbDrone.Core/Download/Clients/` | Reference implementations for all clients |

## Reference App

**Sonarr** — supports 13 torrent clients (Radarr supports 11; Sonarr adds RQBit and Tribler).

## Current State

| Client | SlipStream Status | Notes |
|--------|-------------------|-------|
| Transmission | Fully implemented | Production-ready |
| qBittorrent | Fully implemented | Phase 1 |
| Deluge | Fully implemented | Phase 1 |
| rTorrent | Fully implemented | Phase 1 |
| Vuze | Fully implemented | Phase 2 (wraps Transmission) |
| Flood | Fully implemented | Phase 2 |
| Aria2 | Fully implemented | Phase 2 |
| SABnzbd | Fully implemented | Usenet client |
| uTorrent | Fully implemented | Phase 3 |
| Hadouken | Fully implemented | Phase 3 |
| DownloadStation | Fully implemented | Phase 3 |
| FreeboxDownload | Fully implemented | Phase 3 |
| RQBit | Fully implemented | Phase 3 |
| Tribler | Fully implemented | Phase 3 |
| Mock | Fully implemented | Dev mode only |
| NZBGet | Not yet implemented | Usenet client |

**All torrent clients implemented.** Only NZBGet (usenet) remains.

## Sonarr Torrent Clients → SlipStream Mapping

| # | Client | API Protocol | Default Port | Auth Method | Complexity |
|---|--------|-------------|--------------|-------------|------------|
| 1 | qBittorrent | REST (form/JSON) | 8080 | Cookie or API key | Medium |
| 2 | Deluge | JSON-RPC (`/json`) | 8112 | Password + daemon connect | Medium |
| 3 | rTorrent | XML-RPC | 8080 | HTTP Basic | Medium |
| 4 | Vuze | Transmission RPC | 9091 | Session ID (same as Transmission) | Simple |
| 5 | Aria2 | XML-RPC | 6800 | Secret token | Medium |
| 6 | Flood | REST (JSON) | 3000 | Cookie (user/pass) | Medium |
| 7 | uTorrent | Custom REST | 8080 | Token + cookie | High |
| 8 | Hadouken | JSON-RPC | 7070 | HTTP Basic | Medium |
| 9 | DownloadStation | REST (Synology API) | 5000 | Session-based | High |
| 10 | FreeboxDownload | REST (JSON) | 443 | HMAC-SHA1 challenge-response | High |
| 11 | RQBit | REST (JSON) | 3030 | None | Simple |
| 12 | Tribler | REST (JSON) | 20100 | API key header | Medium |

## Architecture Comparison: Sonarr vs SlipStream

### Sonarr Pattern (C#)
```
TorrentClientBase<TSettings>           # Abstract base (Download, AddFromMagnet, AddFromTorrent)
  └── TransmissionBase                 # Shared Transmission RPC logic
        ├── Transmission               # Transmission-specific (labels, version check)
        └── Vuze                       # Thin override (no labels, path logic, version)
  └── QBittorrent                      # Direct implementation
  └── Deluge                           # Direct implementation
  ...
ITransmissionProxy → TransmissionProxy # HTTP/RPC communication layer
```

### SlipStream Pattern (Go)
```
types.Client                           # Base interface (Type, Test, Connect, Add, List, Get, Remove, Pause, Resume)
types.TorrentClient                    # Extended interface (+AddMagnet, SetSeedLimits, GetTorrentInfo)
  └── transmission.Client              # Full implementation with RPC layer inline
  └── qbittorrent.Client               # Stub
  └── mock.Client                      # Dev mode
factory.go                             # NewClient / NewTorrentClient dispatch
service.go                             # CRUD, AddTorrent, queue ops
```

### Key Mapping: Sonarr Concepts → SlipStream Equivalents

| Sonarr Concept | SlipStream Equivalent | Notes |
|----------------|----------------------|-------|
| `TorrentClientBase.Download()` | `service.AddTorrent()` / `service.AddTorrentWithContent()` | Service layer handles URL vs file |
| `Settings.TvCategory` | `ClientConfig.Category` | Exists in config but unused by Transmission impl |
| `Settings.TvImportedCategory` | Not implemented | Post-import label swap |
| `Settings.TvDirectory` | `AddOptions.DownloadDir` | Per-add override; service constructs `SlipStream/Movies` or `SlipStream/Series` |
| `RemotePathMapping` | Not implemented | Maps remote paths to local |
| `IProxy` (separate class) | Inline in `Client` | SlipStream embeds RPC logic in the client struct |
| `DownloadClientItem` | `types.DownloadItem` | Similar fields |
| `DownloadItemStatus` | `types.Status` | Both have: queued, downloading, paused, completed, seeding, error |
| `HasReachedSeedLimit()` | `types.TorrentInfo` exposes ratio/times | Service checks externally |
| Queue priority (RecentTvPriority) | Not implemented | Could add to AddOptions |
| `SupportsLabels` | Not implemented | Label/category support varies per client |

### What SlipStream's Transmission Implementation Is Missing vs Sonarr

1. **Label/category support** — Sonarr uses `labels` field (Transmission 4.0+) or directory-based filtering; SlipStream ignores `Category`
2. **Post-import category** — Sonarr swaps labels after import; SlipStream has no equivalent
3. **Queue priority** — Sonarr can `queue-move-top`; SlipStream doesn't expose this
4. **Version detection** — Sonarr validates minimum version and toggles label support; SlipStream doesn't check version
5. **Remote path mapping** — Sonarr remaps paths for remote clients; SlipStream assumes local paths
6. **Configurable URL base** — Sonarr allows custom `/transmission/` path; SlipStream hardcodes it

These gaps are **not blockers** for adding new clients but should be addressed incrementally.

## Implementation Plan

### Phase 0: Infrastructure Prep

Prepare the shared infrastructure before implementing individual clients.

**0.1 — Add missing client type constants and update DB migration**

Update `internal/downloader/types/types.go`:
```go
// New constants
ClientTypeVuze          ClientType = "vuze"
ClientTypeAria2         ClientType = "aria2"
ClientTypeFlood         ClientType = "flood"
ClientTypeUTorrent      ClientType = "utorrent"
ClientTypeHadouken      ClientType = "hadouken"
ClientTypeDownloadStation ClientType = "downloadstation"
ClientTypeFreeboxDownload ClientType = "freeboxdownload"
ClientTypeRQBit         ClientType = "rqbit"
ClientTypeTribler       ClientType = "tribler"
```

Add a DB migration to expand the `type` CHECK constraint on `download_clients` to include all new types.

**0.2 — Extend `ClientConfig` for client-specific settings**

Some clients need fields beyond the current `ClientConfig`:
```go
type ClientConfig struct {
    Host       string
    Port       int
    Username   string
    Password   string
    UseSSL     bool
    APIKey     string  // Already exists — used by qBittorrent API key, Tribler, Aria2 secret token
    Category   string  // Already exists — labels/tags per client
    URLBase    string  // NEW — custom URL path (e.g., "/transmission/", "/gui/")
}
```

Add `url_base` column to `download_clients` table (nullable, defaults to empty string). Most clients need configurable URL base paths.

**0.3 — Update factory dispatch**

Extend `factory.go` switch statement and `ImplementedClientTypes()` as each client is completed.

**0.4 — Update frontend client type dropdown**

The frontend download client form needs to list all supported client types. Update the type selector and any client-specific settings fields.

---

### Phase 0.5: Queue Infrastructure Hardening [DONE]

Before adding clients with heavier auth flows, fix two architectural issues in the queue polling system that will cause reliability problems at scale.

**Problem context:** The `QueueBroadcaster` polls every 2s (active) or 30s (idle). Currently, `GetClient()` (`service.go:401`) creates a **new client instance** on every call via the factory, and `GetQueue()` (`queue.go:69`) polls all clients **sequentially** under a single 5-second timeout. This has two consequences:

1. Auth sessions are discarded between polls — Transmission's session ID, qBittorrent's cookie, Deluge's daemon connection are all lost, forcing a full re-auth handshake on every poll cycle.
2. A single slow or unreachable client starves all other clients of their polling budget.

**0.5.1 — Client instance pool with session persistence**

Add a `clientPool` to `Service` that caches live client instances keyed by client ID. This preserves auth state (session IDs, cookies, tokens) across poll cycles.

```go
// In service.go
type Service struct {
    // ... existing fields ...
    clientPoolMu sync.RWMutex
    clientPool   map[int64]Client  // cached live client instances
}
```

Key behaviors:
- `GetClient()` checks the pool first; only creates via factory on cache miss
- `Connect()` / auth is performed once, then reused on subsequent `List()` calls
- Pool entries are **invalidated** when:
  - Client config is updated via `Update()` (config changed, need fresh connection)
  - Client is deleted via `Delete()`
  - `List()` returns a connection/auth error (stale session) — evict and retry once with a fresh instance
- Pool is **cleared entirely** on `SetDB()` (dev mode toggle)
- Each client implementation must handle session expiry gracefully — if a `List()` call gets a 401/409/auth error, it should attempt one internal re-auth before returning an error to the caller. This is already how Transmission works (409 → re-fetch session ID → retry). Other clients should follow the same pattern.

**Why this matters per client:**

| Client | Without pool (every 2s poll) | With pool |
|--------|------------------------------|-----------|
| Transmission | 409 → get session ID → retry = 2 round trips | 1 round trip (session ID cached) |
| qBittorrent | POST `/auth/login` → GET `/torrents/info` = 2 round trips | 1 round trip (cookie persisted) |
| Deluge | `auth.login` → `web.connected` → `web.connect` → `web.update_ui` = 4 round trips | 1 round trip (session persisted) |
| uTorrent | GET `/gui/token.html` → GET `?list=1` = 2 round trips | 1 round trip (token cached) |
| DownloadStation | `SYNO.API.Auth` login → task list = 2 round trips | 1 round trip (session cached) |
| FreeboxDownload | Challenge → HMAC → session token → list = 3 round trips | 1 round trip (session token cached) |
| Aria2, Flood, Hadouken, RQBit, Tribler, rTorrent | Stateless auth (token/basic/none) — no change | No change (already 1 round trip) |

**0.5.2 — Parallel client polling with per-client timeouts**

Refactor `GetQueue()` to poll all enabled clients **concurrently**, each with its own 5-second timeout, instead of sequentially under a shared timeout.

```go
// In queue.go — GetQueue refactored
func (s *Service) GetQueue(ctx context.Context) (*QueueResponse, error) {
    clients, err := s.queries.ListEnabledDownloadClients(ctx)
    if err != nil {
        return nil, err
    }

    type clientResult struct {
        clientID int64
        items    []QueueItem
        err      error
        name     string
    }

    results := make(chan clientResult, len(clients))
    for _, dbClient := range clients {
        if !IsClientTypeImplemented(dbClient.Type) {
            continue
        }
        go func(dc *sqlc.DownloadClient) {
            clientCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
            defer cancel()
            items, err := s.getClientQueue(clientCtx, dc.ID, dc.Name, dc.Type)
            results <- clientResult{clientID: dc.ID, items: items, err: err, name: dc.Name}
        }(dbClient)
    }

    // Collect results
    items := []QueueItem{}
    var clientErrors []ClientError
    for range clients { // or count of dispatched goroutines
        r := <-results
        if r.err != nil {
            clientErrors = append(clientErrors, ClientError{...})
            items = append(items, s.getCachedQueue(r.clientID)...)
            continue
        }
        s.setCachedQueue(r.clientID, r.items)
        items = append(items, r.items...)
    }

    s.enrichQueueItemsWithMappings(ctx, items)
    return &QueueResponse{Items: items, Errors: clientErrors}, nil
}
```

Key behaviors:
- Each client gets its own 5-second timeout — a slow Deluge instance can't starve a fast Transmission instance
- All clients are polled simultaneously — total wall time ≈ slowest single client, not sum of all
- Cache fallback still works per-client on individual failures
- The broadcaster's outer 5s timeout (`broadcaster.go:174`) should be increased to ~8s to allow per-client timeouts to complete (5s poll + margin for enrichment)

**Impact on `CheckForCompletedDownloads` and `CheckForDisappearedDownloads`:**

These functions in `completion.go` have the same sequential-polling pattern. Apply the same parallel treatment:
- `checkClientForCompletions` — fan out per client with per-client timeout
- `collectActiveDownloadIDs` — fan out per client with per-client timeout

**Testing considerations:**
- The mock client is synchronous and instant — it works fine in both sequential and parallel modes
- Integration tests should verify that one client timing out doesn't affect other clients' results
- The cache should be tested: on client error, stale cached data is served rather than dropping items

---

### Phase 1: High-Priority Clients (Most Popular) [DONE]

These are the most commonly used torrent clients. Implement them first.

#### 1.1 — qBittorrent (`internal/downloader/qbittorrent/`)

**Priority: Highest** — Most popular torrent client. Stub already exists.

- **API**: REST — `/api/v2/` endpoints (JSON responses, form-encoded requests)
- **Auth**: POST `/api/v2/auth/login` with username/password → session cookie; OR API key header (v4.1.5+)
- **Key endpoints**:
  - `GET /api/v2/torrents/info` — list torrents (filter by category)
  - `POST /api/v2/torrents/add` — add torrent (multipart form: URLs or file upload)
  - `POST /api/v2/torrents/delete` — remove torrent
  - `POST /api/v2/torrents/pause` / `resume` — pause/resume
  - `POST /api/v2/torrents/setCategory` — assign category
  - `GET /api/v2/app/preferences` — get config (download dir, etc.)
  - `GET /api/v2/app/version` — version check
- **Status mapping**: `stalledUP`/`uploading`/`forcedUP` → seeding; `downloading`/`stalledDL`/`forcedDL`/`metaDL` → downloading; `pausedDL`/`pausedUP` → paused; `queuedDL`/`queuedUP` → queued; `error`/`missingFiles` → error
- **Config fields**: Host, Port, Username, Password, UseSSL, URLBase, Category, APIKey
- **Estimate**: ~400 lines

#### 1.2 — Deluge (`internal/downloader/deluge/`)

**Priority: High** — Very popular, especially on Linux.

- **API**: JSON-RPC over HTTP POST to `{base}/json`
- **Auth**: Call `auth.login` with password → session cookie; then `web.connected` / `web.connect` to ensure daemon connection
- **Key methods**:
  - `web.update_ui` (with filter keys) — list torrents
  - `core.add_torrent_magnet` / `core.add_torrent_file` — add torrent
  - `core.remove_torrent` — remove
  - `core.pause_torrent` / `core.resume_torrent` — pause/resume
  - `core.get_config` — get download dir, seed ratio
  - `label.get_labels` / `label.set_torrent` — category (requires Label plugin)
  - `core.set_torrent_options` — set seed ratio/time limits
- **Request format**: `{"method": "core.add_torrent_magnet", "params": [...], "id": N}`
- **Status mapping**: Deluge states: `Downloading`, `Seeding`, `Paused`, `Checking`, `Queued`, `Error`, `Moving`
- **Config fields**: Host, Port, Password (no username), UseSSL, URLBase, Category
- **Note**: Daemon connection is a two-step process (auth → connect to daemon)
- **Estimate**: ~450 lines

#### 1.3 — rTorrent (`internal/downloader/rtorrent/`)

**Priority: High** — Popular with advanced users, seedbox staple.

- **API**: XML-RPC over HTTP
- **Auth**: HTTP Basic authentication
- **Key methods**:
  - `d.multicall2` — batch query torrent properties (name, hash, size, ratio, state, label, etc.)
  - `load.start` / `load.raw_start` — add torrent from URL / file (with command chaining for label, directory, priority)
  - `d.erase` — remove torrent
  - `d.stop` / `d.start` — pause/resume
  - `d.custom1.set` — set label (category)
  - `system.client_version` — version check
- **XML-RPC format**: Standard XML-RPC encoding (`<methodCall>`, `<params>`, etc.)
- **Config fields**: Host, Port, Username, Password, UseSSL, URLBase (default: `/RPC2`), Category
- **Note**: Requires an XML-RPC library or manual XML construction. Labels use custom field `d.custom1`.
- **Estimate**: ~500 lines (XML-RPC encoding adds overhead)

---

### Phase 2: Medium-Priority Clients [DONE]

#### 2.1 — Vuze (`internal/downloader/vuze/`)

**Priority: Medium** — Uses Transmission RPC protocol, minimal new code.

- **API**: Transmission RPC (identical protocol)
- **Implementation**: Thin wrapper around Transmission client with overrides:
  - `Type()` returns `ClientTypeVuze`
  - Different output path logic for multi-file vs single-file torrents
  - No label support (always directory-based filtering)
  - Version validation uses RPC protocol version (>= 14) not client version string
  - Hash handling: don't lowercase in `Remove()`
- **Config fields**: Same as Transmission
- **Estimate**: ~100 lines (mostly delegates to Transmission)

#### 2.2 — Flood (`internal/downloader/flood/`)

**Priority: Medium** — Modern web UI for rTorrent/qBittorrent.

- **API**: REST (JSON)
- **Auth**: POST `/auth/authenticate` with username/password → session cookie; `GET /auth/verify` to validate
- **Key endpoints**:
  - `GET /torrents` — list torrents
  - `POST /torrents/add-urls` / `add-files` — add torrent
  - `POST /torrents/delete` — remove
  - `PATCH /torrents/tags` — update tags
  - `GET /client/settings` — get download dir
- **Status mapping**: `complete` → completed; `downloading` → downloading; `seeding` → seeding; `stopped` → paused; `checking` → downloading; `error` → error
- **Config fields**: Host, Port, Username, Password, UseSSL, URLBase, Category (used as tag)
- **Estimate**: ~350 lines

#### 2.3 — Aria2 (`internal/downloader/aria2/`)

**Priority: Medium** — Lightweight, popular in Asia.

- **API**: XML-RPC (or JSON-RPC — Aria2 supports both; JSON-RPC is simpler for Go)
- **Auth**: Secret token passed as first parameter in every RPC call: `token:{secret}`
- **Key methods**:
  - `aria2.addUri` — add download from URL/magnet
  - `aria2.addTorrent` — add from torrent file (base64)
  - `aria2.tellActive` / `aria2.tellWaiting` / `aria2.tellStopped` — list downloads
  - `aria2.tellStatus` — get single download
  - `aria2.remove` / `aria2.forceRemove` — remove
  - `aria2.pause` / `aria2.unpause` — pause/resume
  - `aria2.getGlobalOption` — get config (dir)
- **Note**: Aria2 uses GID (unique download ID) not info hash. Need to track GID→hash mapping. Magnet links require polling until metadata is resolved.
- **Config fields**: Host, Port, UseSSL, APIKey (secret token), URLBase (default: `/jsonrpc`)
- **Estimate**: ~400 lines

---

### Phase 3: Lower-Priority Clients [DONE]

#### 3.1 — uTorrent (`internal/downloader/utorrent/`)

- **API**: Custom REST (Web UI API)
- **Auth**: Two-stage — fetch CSRF token from `/gui/token.html`, then use token param + session cookie on all requests
- **Key endpoints**: `?list=1` (list), `?action=add-url` / `add-file` (add), `?action=remove` / `removedata` (remove), `?action=pause` / `unpause` (pause/resume)
- **Note**: Responses use positional arrays (not named fields). Supports differential updates via cache ID.
- **Config fields**: Host, Port, Username, Password, UseSSL, URLBase (default: `/gui/`), Category
- **Estimate**: ~450 lines

#### 3.2 — Hadouken (`internal/downloader/hadouken/`)

- **API**: JSON-RPC
- **Auth**: HTTP Basic
- **Key methods**: `torrents.getByQuery` (list), `torrents.addUrl` / `addFile` (add), `torrents.remove` (remove), `torrents.pause` / `resume`
- **Note**: Torrent data returned as positional arrays with bitwise state flags.
- **Config fields**: Host, Port, Username, Password, UseSSL, URLBase, Category
- **Estimate**: ~300 lines

#### 3.3 — RQBit (`internal/downloader/rqbit/`)

- **API**: REST (JSON)
- **Auth**: None
- **Key endpoints**: `GET /torrents` (list), `POST /torrents` (add), `DELETE /torrents/{id}` (remove), `POST /torrents/{id}/pause` / `start` (pause/resume), `GET /torrents/{id}/stats` (status/progress)
- **Config fields**: Host, Port, UseSSL, URLBase
- **Estimate**: ~250 lines (simplest client)

#### 3.4 — Tribler (`internal/downloader/tribler/`)

- **API**: REST (JSON)
- **Auth**: API key via `X-Api-Key` header
- **Key endpoints**: `GET /downloads` (list), `PUT /downloads` (add), `DELETE /downloads/{hash}` (remove), `PATCH /downloads/{hash}` (pause/resume/set state)
- **Note**: Magnet-only in v8.x (no torrent file upload). Has anonymity hops setting.
- **Config fields**: Host, Port, APIKey, UseSSL, URLBase, Category
- **Estimate**: ~300 lines

#### 3.5 — DownloadStation (`internal/downloader/downloadstation/`)

- **API**: REST (Synology DiskStation Manager API)
- **Auth**: `SYNO.API.Auth` login → session ID cookie
- **Key endpoints**: `SYNO.DownloadStation.Task` (list, create, delete, pause, resume), `SYNO.DownloadStation.Info` (get config)
- **Note**: Most complex client. Multi-version API, shared folder resolution, custom download ID format (serial number hash).
- **Config fields**: Host, Port, Username, Password, UseSSL, Category
- **Estimate**: ~600 lines

#### 3.6 — FreeboxDownload (`internal/downloader/freeboxdownload/`)

- **API**: REST (JSON)
- **Auth**: HMAC-SHA1 challenge-response (AppID + AppToken → challenge → session token)
- **Key endpoints**: `/downloads/` (CRUD), `/downloads/{id}` (get/update/delete)
- **Note**: French ISP hardware. Complex auth flow. Base64-encoded directory paths.
- **Config fields**: Host, Port, APIKey (AppToken), UseSSL, URLBase (default: `/api/v1/`), Category
- **Estimate**: ~500 lines

---

### Phase 4: Frontend & Polish

#### 4.1 — Frontend: Client Type Selector

Update the download client configuration UI to:
- List all new client types in the type dropdown
- Show/hide fields based on selected type (e.g., hide Username for Deluge, show APIKey for Tribler)
- Add URL Base field (hidden for clients that don't need it)

#### 4.2 — Frontend: Client-Specific Defaults

Auto-populate default port and URL base when a client type is selected:

| Client | Default Port | Default URL Base |
|--------|-------------|-----------------|
| Transmission | 9091 | `/transmission/` |
| qBittorrent | 8080 | `/` |
| Deluge | 8112 | `/` |
| rTorrent | 8080 | `/RPC2` |
| Vuze | 9091 | `/transmission/` |
| Flood | 3000 | `/` |
| Aria2 | 6800 | `/jsonrpc` |
| uTorrent | 8080 | `/gui/` |
| Hadouken | 7070 | `/` |
| DownloadStation | 5000 | `/` |
| FreeboxDownload | 443 | `/api/v1/` |
| RQBit | 3030 | `/` |
| Tribler | 20100 | `/` |

#### 4.3 — Test Connection Improvements

Enhance the "Test" endpoint to return client version info and capability flags (e.g., label support, priority support) so the UI can adapt.

---

## Implementation Template

Each new client follows the same pattern. Create `internal/downloader/{client}/client.go`:

```go
package clientname

import "github.com/jatassi/slipstream/internal/downloader/types"

// Compile-time interface check
var _ types.TorrentClient = (*Client)(nil)

type Client struct {
    config     *Config
    httpClient *http.Client
    // session/auth state
}

type Config struct { /* mapped from types.ClientConfig */ }

func NewFromConfig(cfg *types.ClientConfig) *Client { ... }

// --- types.Client ---
func (c *Client) Type() types.ClientType       { return types.ClientTypeX }
func (c *Client) Protocol() types.Protocol     { return types.ProtocolTorrent }
func (c *Client) Test(ctx context.Context) error { ... }
func (c *Client) Connect(ctx context.Context) error { ... }
func (c *Client) Add(ctx context.Context, opts *types.AddOptions) (string, error) { ... }
func (c *Client) List(ctx context.Context) ([]types.DownloadItem, error) { ... }
func (c *Client) Get(ctx context.Context, id string) (*types.DownloadItem, error) { ... }
func (c *Client) Remove(ctx context.Context, id string, deleteFiles bool) error { ... }
func (c *Client) Pause(ctx context.Context, id string) error { ... }
func (c *Client) Resume(ctx context.Context, id string) error { ... }
func (c *Client) GetDownloadDir(ctx context.Context) (string, error) { ... }

// --- types.TorrentClient ---
func (c *Client) AddMagnet(ctx context.Context, magnetURL string, opts *types.AddOptions) (string, error) { ... }
func (c *Client) SetSeedLimits(ctx context.Context, id string, ratio float64, seedTime time.Duration) error { ... }
func (c *Client) GetTorrentInfo(ctx context.Context, id string) (*types.TorrentInfo, error) { ... }
```

## Test Strategy

Every new client and infrastructure change must have tests. The codebase already uses `httptest.NewServer` extensively (see `internal/notification/` and `internal/metadata/`). Follow the same pattern.

### Test template

Copy and adapt `internal/downloader/client_test_template.go.example` for each new client. It provides a complete scaffold with all required test cases.

### Required tests per client (`internal/downloader/{name}/client_test.go`)

| Test | What it verifies | Regression it prevents |
|------|-----------------|----------------------|
| `TestClient_Type` | `Type()` returns correct constant | Wrong type breaks factory dispatch |
| `TestClient_Protocol` | `Protocol()` returns `ProtocolTorrent` | Usenet/torrent confusion in factory |
| `TestClient_Test_Success` | Successful connection test | Test button broken in UI |
| `TestClient_Test_AuthFailure` | Returns `ErrAuthFailed` on 401 | Silent auth failures, confusing UX |
| `TestClient_List` | Status mapping, progress scale, field population | **Queue display broken, completion detection broken** |
| `TestClient_List_Empty` | Returns `[]` not `nil` | Nil pointer in queue iteration |
| `TestClient_Add_URL` | Magnet/HTTP URL add works, returns ID | Grab pipeline broken |
| `TestClient_Add_FileContent` | `.torrent` file upload works | Private tracker grabs broken |
| `TestClient_Remove` | Remove by ID works | Queue cleanup broken |
| `TestClient_Pause` / `Resume` | Pause/resume work | Queue controls broken in UI |
| `TestClient_GetDownloadDir` | Returns correct default dir | Download subdirectory construction broken |
| **`TestClient_SessionReuse`** | Auth happens once, session persists | **Client pool thrashing, 4x API calls per poll** |
| **`TestClient_SessionReauth`** | Re-auths on 401, retries transparently | **Stale sessions cause permanent failures** |

The last two tests (bold) are **critical for client pool compatibility**. They verify that the client holds session state and handles expiry gracefully. Without these, the Phase 0.5 client pool will not work correctly.

### Required tests for Phase 0.5 (`internal/downloader/pool_test.go`, `internal/downloader/queue_test.go`)

| Test | What it verifies |
|------|-----------------|
| `TestClientPool_Hit` | Second `GetClient()` returns same instance |
| `TestClientPool_InvalidateOnUpdate` | `Update()` evicts cached client |
| `TestClientPool_InvalidateOnDelete` | `Delete()` evicts cached client |
| `TestClientPool_InvalidateOnSetDB` | `SetDB()` clears entire pool |
| `TestGetQueue_Parallel` | Multiple clients polled concurrently |
| `TestGetQueue_SlowClient` | One slow client doesn't block others |
| `TestGetQueue_ErrorClient` | One erroring client doesn't affect others |
| `TestGetQueue_CacheFallback` | On error, cached data is returned |

### What NOT to test

- Do not test the factory dispatch for every client type — compile-time `var _ types.TorrentClient` checks + `verify-client-registration.sh` cover this.
- Do not test `service.go` CRUD operations — those are database tests, not client tests.
- Do not write end-to-end tests against real torrent clients — mock servers only.

### Running tests

```bash
# Single client
go test -v ./internal/downloader/qbittorrent/...

# All downloader tests
go test ./internal/downloader/...

# Verbose with race detector (recommended for Phase 0.5)
go test -v -race ./internal/downloader/...
```

## Checklist Per Client

- [ ] Create `internal/downloader/{name}/client.go`
- [ ] Implement all `TorrentClient` interface methods
- [ ] Handle session re-auth internally (e.g., on 401/409, re-authenticate and retry once before returning error) — required for client pool compatibility
- [ ] Add `NewFromConfig` constructor
- [ ] Create `internal/downloader/{name}/client_test.go` (copy from `client_test_template.go.example`)
- [ ] All 14 test cases pass
- [ ] Add case to `factory.go` switch in `NewClient()`
- [ ] Add to `ImplementedClientTypes()` in `factory.go`
- [ ] Add to `validClientTypes` in `service.go`
- [ ] Run `scripts/verify-client-registration.sh {name}` — all green
- [ ] Update DB migration CHECK constraint (if not done in Phase 0)
- [ ] Update frontend type dropdown and field visibility
- [ ] `make lint` passes
- [ ] `go test ./internal/downloader/{name}/...` passes
- [ ] `go test -race ./internal/downloader/...` passes (no race conditions)

## Suggested Implementation Order

| Order | Item | Rationale |
|-------|------|-----------|
| **0** | **Phase 0.5 — Queue infrastructure** | **Must be done before adding any session-based client. Client pool + parallel polling.** |
| 1 | qBittorrent | Highest usage, stub exists, REST API (familiar). First real test of client pool. |
| 2 | Deluge | High usage, JSON-RPC (straightforward). Heaviest auth flow — validates pool design. |
| 3 | Vuze | Trivial — wraps existing Transmission code |
| 4 | rTorrent | High usage among power users, seedbox standard |
| 5 | Flood | Modern REST API, clean implementation |
| 6 | Aria2 | JSON-RPC option simplifies Go implementation |
| 7 | RQBit | Simplest REST client, no auth |
| 8 | Tribler | Simple REST + API key |
| 9 | Hadouken | JSON-RPC, basic auth |
| 10 | uTorrent | Custom auth flow, array parsing |
| 11 | DownloadStation | Most complex (Synology API, shared folders) |
| 12 | FreeboxDownload | Complex auth (HMAC challenge-response), niche market |

**Estimated total: ~4,500 lines of Go across all 12 clients + ~200 lines for queue infrastructure.**
