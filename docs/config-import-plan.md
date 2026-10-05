# Config Import from Sonarr/Radarr — Agent Execution Guide

> **Status: IMPLEMENTED** — All phases complete.

## Context

SlipStream already imports **media** (movies/series/files) from Sonarr/Radarr via `internal/arrimport`. This extends that feature to also import **configuration**: download clients, indexers, notifications, quality profiles, and naming/import settings. Backend only — no frontend changes.

## Execution Strategy

### Subagent Breakdown

Execute in 4 sequential phases. Within each phase, parallelize where indicated.

| Phase | Task | Model | Why |
|-------|------|-------|-----|
| 1a | Create `config_types.go` | Sonnet | Straightforward struct definitions |
| 1b | Create `config_map.go` | **Opus** | Complex mapping logic with many edge cases |
| 1c | Create `config_map_test.go` | Sonnet | Table-driven tests following existing pattern |
| 2a | Modify `reader.go` + `reader_sqlite.go` | Sonnet | SQL queries following existing patterns |
| 2b | Modify `reader_api.go` | Sonnet | API calls following existing patterns |
| 3 | Modify `service.go` (interfaces + preview + import) | **Opus** | Orchestration with many service interactions |
| 4a | Modify `handlers.go` | Sonnet | Simple HTTP handlers |
| 4b | Modify `server.go` + `adapters.go` | Sonnet | Wiring following existing patterns |
| 5 | Create `config_import_test.go` | Sonnet | Integration test with test DB |
| 6 | `make build && make lint` | Sonnet | Validation |

**Parallelization**: 1a+1b+1c can run in parallel. 2a+2b can run in parallel. 4a+4b can run in parallel.

### Pre-Implementation Validation Script

Before writing any code, run this to confirm all assumptions about the codebase still hold:

```bash
# Verify module path
head -1 /Users/jatassi/Developer/SlipStream-v1/go.mod
# Expected: module github.com/slipstream/slipstream

# Verify Reader interface has exactly 7 methods (before we add 5 more)
grep -c 'ctx context.Context' internal/arrimport/reader.go
# Expected: 6 (Validate + 5 Read* + Close doesn't take ctx)

# Verify service struct fields haven't changed
grep -A5 'type Service struct' internal/arrimport/service.go

# Verify CreateClientInput fields
grep -A15 'type CreateClientInput struct' internal/downloader/service.go

# Verify CreateIndexerInput fields
grep -A12 'type CreateIndexerInput struct' internal/indexer/service.go

# Verify notification.CreateInput fields
grep -A18 'type CreateInput struct' internal/notification/types.go

# Verify quality.CreateProfileInput fields (note: UpgradesEnabled is *bool!)
grep -A14 'type CreateProfileInput struct' internal/library/quality/profile.go

# Verify ImportSettings fields
grep -A30 'type ImportSettings struct' internal/import/settings.go

# Verify NotifierType constants
grep 'Notifier.*NotifierType' internal/notification/types/types.go
```

---

## Critical Gotchas (READ BEFORE IMPLEMENTING)

These are verified traps that WILL cause bugs if not handled:

### G1: `CreateProfileInput.UpgradesEnabled` is `*bool`, NOT `bool`
```go
// WRONG:
input.UpgradesEnabled = src.UpgradeAllowed
// CORRECT:
b := src.UpgradeAllowed
input.UpgradesEnabled = &b
```

### G2: Quality profile `Items` must include ALL 17 predefined qualities
SlipStream profiles require all 17 quality items in the `Items` slice. Source profiles only list a subset. Build the full list using `quality.GetQualityByID(id)` for all IDs 1-17, setting `Allowed` based on what was in the source profile.

```go
// Build complete items list
items := make([]quality.QualityItem, 0, 17)
for id := 1; id <= 17; id++ {
    q, ok := quality.GetQualityByID(id)
    if !ok {
        continue
    }
    items = append(items, quality.QualityItem{
        Quality: q,  // Has ID, Name, Source, Resolution, Weight
        Allowed: allowedSet[int64(id)],
    })
}
```

### G3: Group `allowed: false` overrides member items
In source quality profiles, groups can have `allowed: false` which means all qualities inside are disallowed, regardless of their individual `allowed` values.

### G4: Existing `QualityService` interface already exists — use a DIFFERENT name
The arrimport package already has `type QualityService interface { List(...) }` returning `[]*QualityProfile` (thin local type). The new interface must be named `QualityProfileCreateService` to avoid collision.

### G5: `indexer.IndexerDefinition` — NOT `indexertypes.IndexerDefinition`
The indexer package re-exports types. Use only `"github.com/slipstream/slipstream/internal/indexer"` as the import. The `Create` and `List` methods on `indexer.Service` use `indexer.CreateIndexerInput` and `*indexer.IndexerDefinition`.

### G6: Import alias required for `internal/import`
```go
importer "github.com/slipstream/slipstream/internal/import"
```
The package is declared as `package importer` (with a `//nolint:revive` comment).

### G7: Notification columns differ between Sonarr and Radarr DBs
Sonarr has: `OnDownload`, `OnSeriesDelete`, `OnEpisodeFileDelete`, `OnSeriesAdd`, `OnImportComplete`
Radarr has: `OnDownload`, `OnMovieDelete`, `OnMovieFileDelete`, `OnMovieAdded` (NO `OnImportComplete`)

The SQLite reader MUST use different SQL per source type. A unified SELECT will fail.

### G8: NamingConfig columns differ between Sonarr and Radarr DBs
Sonarr: `Id, MultiEpisodeStyle, RenameEpisodes, StandardEpisodeFormat, DailyEpisodeFormat, SeasonFolderFormat, SeriesFolderFormat, AnimeEpisodeFormat, ReplaceIllegalCharacters, SpecialsFolderFormat, ColonReplacementFormat, CustomColonReplacementFormat`
Radarr: `Id, RenameMovies, StandardMovieFormat, MovieFolderFormat, ReplaceIllegalCharacters, ColonReplacementFormat`

The SQLite reader MUST branch by `r.sourceType`.

### G9: `ConfigImportSelections` IDs are SOURCE IDs, not SlipStream IDs
The user selects source entities by their source database ID. The executor must re-read from source and filter.

### G10: Redaction detection happens in SERVICE layer, not reader
The reader returns raw data. The service's `GetConfigPreview` checks for `"********"` in credential fields to mark items as `incomplete`.

### G11: Nil slice → JSON `null` — initialize all slices
```go
// WRONG (sends null to frontend):
report := &ConfigImportReport{}

// CORRECT:
report := &ConfigImportReport{
    Warnings: []string{},
    Errors:   []string{},
}
```

### G12: Unmapped cutoff quality needs fallback
If the source's `Cutoff` quality ID maps to nil (e.g., Raw-HD), fall back to the highest-weight allowed quality in the mapped items list.

### G13: `sabnzbd` and `nzbget` are NOT in SlipStream's `validClientTypes`
`internal/downloader/service.go` has a `validClientTypes` map that `Create()` checks before creating a client. Despite having factory implementations, **`sabnzbd` and `nzbget` are NOT in this map**. `nzbget` additionally has no working factory implementation ("not yet implemented"). The import must treat both as unsupported (map to `""`) to avoid `ErrUnsupportedClient` from `Create`.

### G14: Download client type values use no underscores
SlipStream's `validClientTypes` uses `"downloadstation"` and `"freeboxdownload"` (no underscores). NOT `"download_station"` or `"freebox_download"`.

### G15: `colonReplacementMap` and `multiEpisodeStyleMap` must use named string types
The target types are `renamer.ColonReplacement` and `renamer.MultiEpisodeStyle` (named string types). Go does NOT allow implicit conversion from `string` to named string types. Maps must be typed: `map[int]renamer.ColonReplacement` and `map[int]renamer.MultiEpisodeStyle`.

### G16: `notificationTypeMap` must use `notification.NotifierType` type
`CreateInput.Type` is `notification.NotifierType` (a named string type). The map should be `map[string]notification.NotifierType` with values like `notification.NotifierDiscord`, or use explicit type conversion at the call site: `notification.NotifierType(value)`.

### G17: Sonarr/Radarr API returns `enable` (not `enabled`) for download clients
The C# property is `Enable` which serializes to JSON `"enable"`. The API reader's anonymous struct must use `Enable bool` or tag with `` `json:"enable"` ``, NOT `Enabled`.

---

## Source-to-Target Mapping (STTM)

### 1. Download Clients

| Source (Sonarr/Radarr) | Target (SlipStream `CreateClientInput`) | Notes |
|---|---|---|
| `Name` | `Name` | Direct |
| `Implementation` | `Type` | Via `downloadClientTypeMap` (lowercase) |
| `Settings.host` | `Host` | Parsed from Settings JSON |
| `Settings.port` | `Port` | `int` — parse carefully, may be string in edge cases |
| `Settings.username` | `Username` | |
| `Settings.password` | `Password` | **Plaintext in DB; `********` in API** |
| `Settings.useSsl` | `UseSSL` | |
| `Settings.urlBase` | `URLBase` | |
| `Settings.tvCategory` (Sonarr) / `Settings.movieCategory` (Radarr) | `Category` | Source-type dependent key name |
| `Enable` (int 0/1) | `Enabled` (bool) | DB column is `Enable` (no 'd'); API JSON is `enable` (G17) |
| `Priority` | `Priority` | |
| `RemoveCompletedDownloads` + `RemoveFailedDownloads` | `CleanupMode` | See mapping below |
| — | `ImportDelaySeconds` | Default `0` |
| — | `SeedRatioTarget` | Default `nil` |
| — | `CleanupMode` | Default `"leave"` if not mapped |

**CleanupMode mapping:**
```
RemoveCompletedDownloads=true  → "delete_after_import"
RemoveCompletedDownloads=false → "leave"
(RemoveFailedDownloads is not mappable to SlipStream — ignore with info log)
```

**Implementation type map** (`downloadClientTypeMap`):
```go
var downloadClientTypeMap = map[string]string{
    "Transmission":       "transmission",
    "QBittorrent":        "qbittorrent",
    "Deluge":             "deluge",
    "RTorrent":           "rtorrent",
    "Vuze":               "vuze",
    "Aria2":              "aria2",
    "Flood":              "flood",
    "UTorrent":           "utorrent",
    "Hadouken":           "hadouken",
    "DownloadStation":    "downloadstation",    // G14: no underscore
    "FreeboxDownload":    "freeboxdownload",    // G14: no underscore
    // Unsupported (map to empty string):
    "Sabnzbd":            "",  // G13: not in validClientTypes
    "NzbGet":             "",  // G13: not in validClientTypes, factory unimplemented
    "TorrentBlackhole":   "",
    "UsenetBlackhole":    "",
    "NzbVortex":          "",
    "UsenetDownloadStation": "",
    "PneumaticClient":    "",
}
```

**Real data from production DBs** (use for test fixtures):
- Sonarr: 2 Transmission clients (seedbox host `111.nl116.seedit4.me:8101`, local `localhost:9091`), category key is `tvCategory`
- Radarr: 2 Transmission clients (same hosts), category key is `movieCategory`

### 2. Indexers

| Source | Target (`CreateIndexerInput`) | Notes |
|---|---|---|
| `Name` | `Name` | |
| `Implementation` | `DefinitionID` | Via mapping function |
| `Settings` JSON | `Settings` JSON | Pass through; rename `baseUrl`→`url` if present |
| `Settings.categories` | `Categories` | Direct `[]int` from Settings JSON |
| `EnableRss` | `RssEnabled` | `*bool` — must use pointer |
| `EnableAutomaticSearch` | `AutoSearchEnabled` | `*bool` — must use pointer |
| always `true` | `SupportsMovies`, `SupportsTV` | |
| `Priority` | `Priority` | |
| — | `Enabled` | DB has NO `Enable` column for indexers — always set `true`. API has `enable` field via ProviderResource but it's not in the DB schema |

**Indexer definition ID mapping:**
```go
func indexerImplementationToDefinitionID(impl string) string {
    // Known direct mappings
    known := map[string]string{
        "Torznab":    "torznab",
        "Newznab":    "newznab",
        "IPTorrents": "iptorrents",
        "Nyaa":       "nyaa",
    }
    if id, ok := known[impl]; ok {
        return id
    }
    // Fallback: lowercase the implementation name (works for most cardigann defs)
    return strings.ToLower(impl)
}
```

**Indexer Settings JSON translation:**
The source Settings JSON contains `baseUrl`, `apiKey`, `apiPath`, `categories`, etc. SlipStream's cardigann indexer settings vary by definition, but the common pattern is:
- Keep `apiKey` as-is (it's the same field name)
- Rename `baseUrl` → `url` (cardigann convention)
- Extract `categories` from Settings into the top-level `Categories` field
- Pass remaining settings through as-is

```go
func translateIndexerSettings(settings json.RawMessage) (json.RawMessage, []int, []string) {
    var parsed map[string]interface{}
    json.Unmarshal(settings, &parsed)

    // Extract categories
    var categories []int
    if cats, ok := parsed["categories"].([]interface{}); ok {
        for _, c := range cats {
            if n, ok := c.(float64); ok {
                categories = append(categories, int(n))
            }
        }
    }
    delete(parsed, "categories")
    delete(parsed, "animeCategories") // Not used in SlipStream

    // Rename baseUrl → url
    if base, ok := parsed["baseUrl"]; ok {
        parsed["url"] = base
        delete(parsed, "baseUrl")
    }

    // Remove non-setting fields (present in Sonarr/Radarr but not cardigann settings)
    delete(parsed, "apiPath")
    delete(parsed, "multiLanguages")
    delete(parsed, "failDownloads")
    delete(parsed, "animeStandardFormatSearch")
    delete(parsed, "removeYear")                              // Radarr-specific
    delete(parsed, "requiredFlags")                           // Radarr-specific
    delete(parsed, "minimumSeeders")                          // Sonarr/Radarr-specific
    delete(parsed, "seedCriteria")                            // Sonarr/Radarr-specific
    delete(parsed, "rejectBlocklistedTorrentHashesWhileGrabbing") // Sonarr/Radarr-specific

    result, _ := json.Marshal(parsed)
    return result, categories, nil
}
```

**Real data**: All indexers in both DBs are either `Torznab` or `Torrentleech` implementation.

### 3. Notifications

| Source | Target (`notification.CreateInput`) | Notes |
|---|---|---|
| `Name` | `Name` | |
| `Implementation` | `Type` | Via `notificationTypeMap` — cast with `notification.NotifierType(value)` (G16) |
| `Settings` JSON | `Settings` JSON | Re-key per type |
| `OnGrab` | `OnGrab` | |
| `OnDownload` (Sonarr) / `OnDownload` (Radarr) | `OnImport` | Both use `OnDownload` column |
| `OnUpgrade` | `OnUpgrade` | |
| `OnSeriesAdd` (Sonarr) / `OnMovieAdded` (Radarr) | `OnSeriesAdded` / `OnMovieAdded` | Map to correct target field based on source type |
| `OnSeriesDelete` (Sonarr) / `OnMovieDelete` (Radarr) | `OnSeriesDeleted` / `OnMovieDeleted` | |
| `OnHealthIssue` | `OnHealthIssue` | |
| `OnHealthRestored` | `OnHealthRestored` | |
| `OnApplicationUpdate` | `OnAppUpdate` | |
| `IncludeHealthWarnings` | `IncludeHealthWarnings` | |

**Notification type map** (`notificationTypeMap`) — G16: use `notification.NotifierType` type:
```go
var notificationTypeMap = map[string]notification.NotifierType{
    "Discord":      notification.NotifierDiscord,
    "Telegram":     notification.NotifierTelegram,
    "Webhook":      notification.NotifierWebhook,
    "Email":        notification.NotifierEmail,
    "Slack":        notification.NotifierSlack,
    "Pushover":     notification.NotifierPushover,
    "Gotify":       notification.NotifierGotify,
    "Ntfy":         notification.NotifierNtfy,
    "Apprise":      notification.NotifierApprise,
    "Pushbullet":   notification.NotifierPushbullet,
    "Join":         notification.NotifierJoin,
    "Prowl":        notification.NotifierProwl,
    "Simplepush":   notification.NotifierSimplepush,
    "Signal":       notification.NotifierSignal,
    "CustomScript": notification.NotifierCustomScript,
    "PlexServer":   notification.NotifierPlex,
}
// Check unsupported separately (not in map = unsupported)
```

**Settings re-keying** — only rename keys that differ; pass everything else through:
```go
var notificationSettingsKeyMap = map[string]map[string]string{
    "discord":  {"webHookUrl": "webhookUrl"},
    "slack":    {"webHookUrl": "webhookUrl"},
    "plex":     {"authToken": "token"},
    "email":    {"server": "host", "requireEncryption": "useSsl"},
    "gotify":   {"server": "url", "appToken": "token"},
    "ntfy":     {"server": "url"},
    "pushover": {"appToken": "token"},
}
```

**Real data**: Both DBs have PlexServer and Discord notifications.

### 4. Quality Profiles

| Source | Target (`quality.CreateProfileInput`) | Notes |
|---|---|---|
| `Name` | `Name` | |
| `UpgradeAllowed` (int 0/1) | `UpgradesEnabled` (**`*bool`**) | G1: MUST use pointer |
| `Cutoff` (source quality ID) | `Cutoff` (SlipStream quality ID) | Map via `MapQualityID`; G12: fallback if unmapped |
| `Items` (nested JSON) | `Items` (`[]quality.QualityItem`) | Flatten + map; G2: must include all 17; G3: group override |
| — | `UpgradeStrategy` | Default `"balanced"` |
| — | `CutoffOverridesStrategy` | Default `false` |
| — | `AllowAutoApprove` | Default `false` |
| — | `HDRSettings` etc. | Default empty `quality.AttributeSettings{}` |

**Flattening algorithm** (critical — see G2 and G3):

```go
func flattenQualityProfileItems(sourceType SourceType, itemsJSON json.RawMessage, cutoffID int) ([]quality.QualityItem, int, []string) {
    var warnings []string

    // Step 1: Parse source items and extract allowed set
    type sourceItem struct {
        Quality *struct {
            ID   int    `json:"id"`
            Name string `json:"name"`
        } `json:"quality"`
        Items   []sourceItem `json:"items"`
        Allowed bool         `json:"allowed"`
    }
    var srcItems []sourceItem
    json.Unmarshal(itemsJSON, &srcItems)

    // Step 2: Walk tree, collecting allowed source quality IDs
    // If group.Allowed == false, all children are disallowed (G3)
    allowedSourceIDs := map[int]bool{}
    var walk func(items []sourceItem, parentAllowed bool)
    walk = func(items []sourceItem, parentAllowed bool) {
        for _, item := range items {
            if item.Quality != nil {
                allowedSourceIDs[item.Quality.ID] = parentAllowed && item.Allowed
            }
            if len(item.Items) > 0 {
                walk(item.Items, parentAllowed && item.Allowed)
            }
        }
    }
    walk(srcItems, true)

    // Step 3: Map source quality IDs → SlipStream quality IDs
    allowedSSIDs := map[int]bool{}
    for srcID, allowed := range allowedSourceIDs {
        if ssID := MapQualityID(sourceType, srcID, ""); ssID != nil {
            allowedSSIDs[int(*ssID)] = allowed
        }
    }

    // Step 4: Build FULL 17-item list (G2)
    items := make([]quality.QualityItem, 0, 17)
    for id := 1; id <= 17; id++ {
        q, ok := quality.GetQualityByID(id)
        if !ok { continue }
        items = append(items, quality.QualityItem{
            Quality: q,
            Allowed: allowedSSIDs[id],
        })
    }

    // Step 5: Map cutoff (G12)
    mappedCutoff := 0
    if ssID := MapQualityID(sourceType, cutoffID, ""); ssID != nil {
        mappedCutoff = int(*ssID)
    } else {
        // Fallback: highest-weight allowed quality
        for i := len(items) - 1; i >= 0; i-- {
            if items[i].Allowed {
                mappedCutoff = items[i].Quality.ID
                break
            }
        }
    }

    return items, mappedCutoff, warnings
}
```

### 5. Naming/Import Settings

| Source (NamingConfig) | Target (`importer.ImportSettings` field) | Notes |
|---|---|---|
| `RenameEpisodes` (Sonarr) | `RenameEpisodes` | |
| `ReplaceIllegalCharacters` | `ReplaceIllegalCharacters` | |
| `ColonReplacementFormat` (int) | `ColonReplacement` (`renamer.ColonReplacement`) | Map below |
| `MultiEpisodeStyle` (int, Sonarr) | `MultiEpisodeStyle` (`renamer.MultiEpisodeStyle`) | Map below |
| `StandardEpisodeFormat` (Sonarr) | `StandardEpisodeFormat` | Tokens are compatible |
| `DailyEpisodeFormat` (Sonarr) | `DailyEpisodeFormat` | |
| `AnimeEpisodeFormat` (Sonarr) | `AnimeEpisodeFormat` | |
| `SeriesFolderFormat` (Sonarr) | `SeriesFolderFormat` | |
| `SeasonFolderFormat` (Sonarr) | `SeasonFolderFormat` | |
| `SpecialsFolderFormat` (Sonarr) | `SpecialsFolderFormat` | |
| `RenameMovies` (Radarr) | `RenameMovies` | |
| `StandardMovieFormat` (Radarr) | `MovieFileFormat` | |
| `MovieFolderFormat` (Radarr) | `MovieFolderFormat` | |

```go
// G15: Must use named string types, not plain string
var colonReplacementMap = map[int]renamer.ColonReplacement{
    0: renamer.ColonDelete, 1: renamer.ColonDash, 2: renamer.ColonSpaceDash,
    3: renamer.ColonSpaceDashSpace, 4: renamer.ColonSmart,
}

var multiEpisodeStyleMap = map[int]renamer.MultiEpisodeStyle{
    0: renamer.StyleExtend, 1: renamer.StyleDuplicate, 2: renamer.StyleRepeat,
    3: renamer.StyleScene, 4: renamer.StyleRange, 5: renamer.StylePrefixedRange,
}
```

### Not Imported (no SlipStream equivalent)

Tags, custom formats, release profiles, delay profiles, remote path mappings, import lists, UI config, users, metadata consumers.

---

## Exact Import Paths

```go
"github.com/slipstream/slipstream/internal/downloader"
"github.com/slipstream/slipstream/internal/indexer"
"github.com/slipstream/slipstream/internal/notification"
"github.com/slipstream/slipstream/internal/library/quality"
importer "github.com/slipstream/slipstream/internal/import"
"github.com/slipstream/slipstream/internal/import/renamer"
```

---

## Implementation Phases

### Phase 1: Types and Mapping (parallel: 1a + 1b + 1c) ✅

#### 1a: `internal/arrimport/config_types.go` (Sonnet)

Create this file with all source types and request/response types.

**Source types** — internal transfer types (JSON tags for API reader, field names for readability; SQLite reader scans by column position, API reader uses anonymous structs):

```go
type SourceDownloadClient struct {
    ID                       int64           `json:"id"`
    Name                     string          `json:"name"`
    Implementation           string          `json:"implementation"`
    Settings                 json.RawMessage `json:"settings"`
    Enabled                  bool            `json:"enabled"`
    Priority                 int             `json:"priority"`
    RemoveCompletedDownloads bool            `json:"removeCompletedDownloads"`
    RemoveFailedDownloads    bool            `json:"removeFailedDownloads"`
}

type SourceIndexer struct {
    ID                      int64           `json:"id"`
    Name                    string          `json:"name"`
    Implementation          string          `json:"implementation"`
    Settings                json.RawMessage `json:"settings"`
    EnableRss               bool            `json:"enableRss"`
    EnableAutomaticSearch   bool            `json:"enableAutomaticSearch"`
    EnableInteractiveSearch bool            `json:"enableInteractiveSearch"`
    Priority                int             `json:"priority"`
}

type SourceNotification struct {
    ID                    int64           `json:"id"`
    Name                  string          `json:"name"`
    Implementation        string          `json:"implementation"`
    Settings              json.RawMessage `json:"settings"`
    OnGrab                bool            `json:"onGrab"`
    OnDownload            bool            `json:"onDownload"`
    OnUpgrade             bool            `json:"onUpgrade"`
    OnHealthIssue         bool            `json:"onHealthIssue"`
    IncludeHealthWarnings bool            `json:"includeHealthWarnings"`
    OnHealthRestored      bool            `json:"onHealthRestored"`
    OnApplicationUpdate   bool            `json:"onApplicationUpdate"`
    // Sonarr-specific (zero for Radarr)
    OnSeriesAdd    bool `json:"onSeriesAdd"`
    OnSeriesDelete bool `json:"onSeriesDelete"`
    // Radarr-specific (zero for Sonarr)
    OnMovieAdded  bool `json:"onMovieAdded"`
    OnMovieDelete bool `json:"onMovieDelete"`
}

type SourceNamingConfig struct {
    // Shared
    ReplaceIllegalCharacters bool   `json:"replaceIllegalCharacters"`
    ColonReplacementFormat   int    `json:"colonReplacementFormat"`
    // Sonarr-only
    RenameEpisodes        bool   `json:"renameEpisodes"`
    MultiEpisodeStyle     int    `json:"multiEpisodeStyle"`
    StandardEpisodeFormat string `json:"standardEpisodeFormat"`
    DailyEpisodeFormat    string `json:"dailyEpisodeFormat"`
    AnimeEpisodeFormat    string `json:"animeEpisodeFormat"`
    SeriesFolderFormat    string `json:"seriesFolderFormat"`
    SeasonFolderFormat    string `json:"seasonFolderFormat"`
    SpecialsFolderFormat  string `json:"specialsFolderFormat"`
    // Radarr-only
    RenameMovies        bool   `json:"renameMovies"`
    StandardMovieFormat string `json:"standardMovieFormat"`
    MovieFolderFormat   string `json:"movieFolderFormat"`
}

type SourceQualityProfileFull struct {
    ID             int64           `json:"id"`
    Name           string          `json:"name"`
    Cutoff         int             `json:"cutoff"`
    UpgradeAllowed bool            `json:"upgradeAllowed"`
    Items          json.RawMessage `json:"items"`
}
```

**Preview/report types** — initialize ALL slice fields (G11):

```go
type ConfigPreview struct {
    DownloadClients []ConfigPreviewItem  `json:"downloadClients"`
    Indexers        []ConfigPreviewItem  `json:"indexers"`
    Notifications   []ConfigPreviewItem  `json:"notifications"`
    QualityProfiles []ConfigPreviewItem  `json:"qualityProfiles"`
    NamingConfig    *NamingConfigPreview `json:"namingConfig,omitempty"`
    Warnings        []string             `json:"warnings"`
}

type ConfigPreviewItem struct {
    SourceID     int64  `json:"sourceId"`
    SourceName   string `json:"sourceName"`
    SourceType   string `json:"sourceType"`   // e.g. "Transmission", "Torznab"
    MappedType   string `json:"mappedType"`   // e.g. "transmission", "torznab"
    Status       string `json:"status"`       // "new", "duplicate", "unsupported", "incomplete"
    StatusReason string `json:"statusReason,omitempty"`
}

type NamingConfigPreview struct {
    Source SourceNamingConfig `json:"source"`
    Status string             `json:"status"` // "different", "same"
}

type ConfigImportSelections struct {
    DownloadClientIDs []int64 `json:"downloadClientIds"`  // SOURCE IDs (G9)
    IndexerIDs        []int64 `json:"indexerIds"`
    NotificationIDs   []int64 `json:"notificationIds"`
    QualityProfileIDs []int64 `json:"qualityProfileIds"`
    ImportNamingConfig bool   `json:"importNamingConfig"`
}

type ConfigImportReport struct {
    DownloadClientsCreated int      `json:"downloadClientsCreated"`
    DownloadClientsSkipped int      `json:"downloadClientsSkipped"`
    IndexersCreated        int      `json:"indexersCreated"`
    IndexersSkipped        int      `json:"indexersSkipped"`
    NotificationsCreated   int      `json:"notificationsCreated"`
    NotificationsSkipped   int      `json:"notificationsSkipped"`
    QualityProfilesCreated int      `json:"qualityProfilesCreated"`
    QualityProfilesSkipped int      `json:"qualityProfilesSkipped"`
    NamingConfigImported   bool     `json:"namingConfigImported"`
    Warnings               []string `json:"warnings"`
    Errors                 []string `json:"errors"`
}
```

Constructor:
```go
func newConfigImportReport() *ConfigImportReport {
    return &ConfigImportReport{Warnings: []string{}, Errors: []string{}}
}
```

#### 1b: `internal/arrimport/config_map.go` (Opus — complex logic)

All static mapping tables and translation functions. See STTM section above for exact map contents.

Key functions to implement:
- `downloadClientTypeMap` (var)
- `notificationTypeMap` (var — `map[string]notification.NotifierType`, G16)
- `colonReplacementMap` (var — `map[int]renamer.ColonReplacement`, G15)
- `multiEpisodeStyleMap` (var — `map[int]renamer.MultiEpisodeStyle`, G15)
- `notificationSettingsKeyMap` (var)
- `indexerImplementationToDefinitionID(impl string) string`
- `translateDownloadClientSettings(impl string, settings json.RawMessage, sourceType SourceType) (host string, port int, username, password string, useSsl bool, apiKey, category, urlBase string, warnings []string)` — parse JSON object, extract fields, handle `tvCategory` vs `movieCategory` based on sourceType
- `translateIndexerSettings(settings json.RawMessage) (json.RawMessage, []int, []string)` — extract categories, rename baseUrl→url
- `translateNotificationSettings(impl string, settings json.RawMessage) (json.RawMessage, []string)` — apply key renames from `notificationSettingsKeyMap`
- `flattenQualityProfileItems(sourceType SourceType, itemsJSON json.RawMessage, cutoffID int) ([]quality.QualityItem, int, []string)` — see algorithm above
- `translateNamingConfig(src *SourceNamingConfig, sourceType SourceType, current *importer.ImportSettings) (*importer.ImportSettings, []string)` — merge source naming fields into current settings (only overwrite the fields from the source, leave others as-is)
- `hasRedactedCredentials(settings json.RawMessage) bool` — check for `"********"` in any string value

#### 1c: `internal/arrimport/config_map_test.go` (Sonnet)

Follow the exact pattern from `quality_map_test.go`. Table-driven tests.

```go
package arrimport

import (
    "encoding/json"
    "testing"

    "github.com/slipstream/slipstream/internal/import/renamer"
    "github.com/slipstream/slipstream/internal/notification"
)

func TestDownloadClientTypeMap(t *testing.T) {
    tests := []struct{ impl, expected string }{
        {"Transmission", "transmission"},
        {"QBittorrent", "qbittorrent"},
        {"Deluge", "deluge"},
        {"DownloadStation", "downloadstation"},   // G14: no underscore
        {"FreeboxDownload", "freeboxdownload"},   // G14: no underscore
        {"Sabnzbd", ""},           // G13: unsupported (not in validClientTypes)
        {"NzbGet", ""},            // G13: unsupported (not in validClientTypes)
        {"TorrentBlackhole", ""},  // unsupported
        {"UsenetBlackhole", ""},   // unsupported
    }
    for _, tt := range tests {
        if got := downloadClientTypeMap[tt.impl]; got != tt.expected {
            t.Errorf("downloadClientTypeMap[%q] = %q, want %q", tt.impl, got, tt.expected)
        }
    }
}

func TestNotificationTypeMap(t *testing.T) {
    tests := []struct{
        impl     string
        expected notification.NotifierType
        exists   bool
    }{
        {"Discord", notification.NotifierDiscord, true},
        {"PlexServer", notification.NotifierPlex, true},
        {"CustomScript", notification.NotifierCustomScript, true},
        {"Emby", "", false},  // unsupported — not in map
    }
    for _, tt := range tests {
        got, ok := notificationTypeMap[tt.impl]
        if ok != tt.exists {
            t.Errorf("notificationTypeMap[%q] exists=%v, want %v", tt.impl, ok, tt.exists)
        }
        if ok && got != tt.expected {
            t.Errorf("notificationTypeMap[%q] = %q, want %q", tt.impl, got, tt.expected)
        }
    }
}

func TestColonReplacementMap(t *testing.T) {
    tests := []struct{
        input    int
        expected renamer.ColonReplacement
    }{
        {0, renamer.ColonDelete}, {1, renamer.ColonDash}, {2, renamer.ColonSpaceDash},
        {3, renamer.ColonSpaceDashSpace}, {4, renamer.ColonSmart},
    }
    for _, tt := range tests {
        if got := colonReplacementMap[tt.input]; got != tt.expected {
            t.Errorf("colonReplacementMap[%d] = %q, want %q", tt.input, got, tt.expected)
        }
    }
}

func TestFlattenQualityProfileItems(t *testing.T) {
    // Real Sonarr "HD-1080p" profile items (ID=4, cutoff=9 HDTV-1080p)
    // Contains groups and individual items
    itemsJSON := json.RawMessage(`[
        {"quality":{"id":0,"name":"Unknown"},"items":[],"allowed":false},
        {"quality":{"id":1,"name":"SDTV"},"items":[],"allowed":false},
        {"id":1000,"name":"WEB 480p","items":[
            {"quality":{"id":12,"name":"WEBRip-480p"},"items":[],"allowed":false},
            {"quality":{"id":8,"name":"WEBDL-480p"},"items":[],"allowed":false}
        ],"allowed":false},
        {"quality":{"id":2,"name":"DVD"},"items":[],"allowed":false},
        {"quality":{"id":4,"name":"HDTV-720p"},"items":[],"allowed":false},
        {"quality":{"id":9,"name":"HDTV-1080p"},"items":[],"allowed":true},
        {"id":1001,"name":"WEB 720p","items":[
            {"quality":{"id":14,"name":"WEBRip-720p"},"items":[],"allowed":false},
            {"quality":{"id":5,"name":"WEBDL-720p"},"items":[],"allowed":false}
        ],"allowed":false},
        {"id":1002,"name":"WEB 1080p","items":[
            {"quality":{"id":15,"name":"WEBRip-1080p"},"items":[],"allowed":true},
            {"quality":{"id":3,"name":"WEBDL-1080p"},"items":[],"allowed":true}
        ],"allowed":true},
        {"quality":{"id":7,"name":"Bluray-1080p"},"items":[],"allowed":true},
        {"quality":{"id":6,"name":"Bluray-720p"},"items":[],"allowed":false},
        {"quality":{"id":20,"name":"Bluray-1080p Remux"},"items":[],"allowed":false},
        {"quality":{"id":16,"name":"HDTV-2160p"},"items":[],"allowed":false},
        {"id":1003,"name":"WEB 2160p","items":[
            {"quality":{"id":17,"name":"WEBRip-2160p"},"items":[],"allowed":false},
            {"quality":{"id":18,"name":"WEBDL-2160p"},"items":[],"allowed":false}
        ],"allowed":false},
        {"quality":{"id":19,"name":"Bluray-2160p"},"items":[],"allowed":false},
        {"quality":{"id":21,"name":"Bluray-2160p Remux"},"items":[],"allowed":false}
    ]`)

    items, cutoff, warnings := flattenQualityProfileItems(SourceTypeSonarr, itemsJSON, 9)

    // Must have all 17 qualities
    if len(items) != 17 {
        t.Fatalf("expected 17 items, got %d", len(items))
    }

    // Verify specific allowed states
    allowedIDs := map[int]bool{}
    for _, item := range items {
        if item.Allowed {
            allowedIDs[item.Quality.ID] = true
        }
    }

    // HDTV-1080p (source 9 → SS 8) should be allowed
    if !allowedIDs[8] {
        t.Error("HDTV-1080p (SS ID 8) should be allowed")
    }
    // WEBRip-1080p (source 15 → SS 9) should be allowed (in allowed group)
    if !allowedIDs[9] {
        t.Error("WEBRip-1080p (SS ID 9) should be allowed")
    }
    // WEBDL-1080p (source 3 → SS 10) should be allowed
    if !allowedIDs[10] {
        t.Error("WEBDL-1080p (SS ID 10) should be allowed")
    }
    // Bluray-1080p (source 7 → SS 11) should be allowed
    if !allowedIDs[11] {
        t.Error("Bluray-1080p (SS ID 11) should be allowed")
    }
    // SDTV (source 1 → SS 1) should NOT be allowed
    if allowedIDs[1] {
        t.Error("SDTV (SS ID 1) should NOT be allowed")
    }

    // Cutoff: source 9 (HDTV-1080p) → SS 8
    if cutoff != 8 {
        t.Errorf("expected cutoff 8 (HDTV-1080p), got %d", cutoff)
    }

    _ = warnings
}

func TestFlattenQualityProfileItems_GroupDisabled(t *testing.T) {
    // Group with allowed=false should override member allowed=true (G3)
    itemsJSON := json.RawMessage(`[
        {"id":1002,"name":"WEB 1080p","items":[
            {"quality":{"id":15,"name":"WEBRip-1080p"},"items":[],"allowed":true},
            {"quality":{"id":3,"name":"WEBDL-1080p"},"items":[],"allowed":true}
        ],"allowed":false}
    ]`)

    items, _, _ := flattenQualityProfileItems(SourceTypeSonarr, itemsJSON, 0)

    for _, item := range items {
        if item.Quality.ID == 9 && item.Allowed { // WEBRip-1080p → SS 9
            t.Error("WEBRip-1080p should be disallowed (parent group disabled)")
        }
        if item.Quality.ID == 10 && item.Allowed { // WEBDL-1080p → SS 10
            t.Error("WEBDL-1080p should be disallowed (parent group disabled)")
        }
    }
}

func TestTranslateDownloadClientSettings(t *testing.T) {
    // Real Sonarr Transmission settings
    settings := json.RawMessage(`{
        "host": "111.nl116.seedit4.me",
        "port": 8101,
        "useSsl": false,
        "urlBase": "/transmission/",
        "username": "seedit4me",
        "password": "eWdiNrG*Rmww",
        "tvCategory": "sonarr",
        "recentTvPriority": 0,
        "olderTvPriority": 0,
        "addPaused": false
    }`)

    host, port, username, password, useSsl, _, category, urlBase, warnings :=
        translateDownloadClientSettings("Transmission", settings, SourceTypeSonarr)

    if host != "111.nl116.seedit4.me" { t.Errorf("host = %q", host) }
    if port != 8101 { t.Errorf("port = %d", port) }
    if username != "seedit4me" { t.Errorf("username = %q", username) }
    if password != "eWdiNrG*Rmww" { t.Errorf("password = %q", password) }
    if useSsl { t.Error("useSsl should be false") }
    if category != "sonarr" { t.Errorf("category = %q", category) }
    if urlBase != "/transmission/" { t.Errorf("urlBase = %q", urlBase) }
    if len(warnings) != 0 { t.Errorf("unexpected warnings: %v", warnings) }
}

func TestHasRedactedCredentials(t *testing.T) {
    redacted := json.RawMessage(`{"host":"localhost","password":"********"}`)
    if !hasRedactedCredentials(redacted) {
        t.Error("should detect redacted credentials")
    }
    clean := json.RawMessage(`{"host":"localhost","password":"real"}`)
    if hasRedactedCredentials(clean) {
        t.Error("should not flag clean credentials")
    }
}
```

### Phase 2: Reader Implementation (parallel: 2a + 2b) ✅

#### 2a: Modify `reader.go` + `reader_sqlite.go` (Sonnet)

**`reader.go`** — Add 5 methods to the `Reader` interface:
```go
ReadDownloadClients(ctx context.Context) ([]SourceDownloadClient, error)
ReadIndexers(ctx context.Context) ([]SourceIndexer, error)
ReadNotifications(ctx context.Context) ([]SourceNotification, error)
ReadQualityProfilesFull(ctx context.Context) ([]SourceQualityProfileFull, error)
ReadNamingConfig(ctx context.Context) (*SourceNamingConfig, error)
```

**`reader_sqlite.go`** — Follow Pattern A (see existing `ReadRootFolders` at line 55). Key detail: Notifications and NamingConfig MUST branch on `r.sourceType` (G7, G8).

Exact SQL for `ReadNotifications`:
```sql
-- Sonarr:
SELECT Id, Name, Implementation, Settings, OnGrab, OnDownload, OnUpgrade,
       OnHealthIssue, IncludeHealthWarnings, OnHealthRestored, OnApplicationUpdate,
       OnSeriesAdd, OnSeriesDelete
FROM Notifications

-- Radarr:
SELECT Id, Name, Implementation, Settings, OnGrab, OnDownload, OnUpgrade,
       OnHealthIssue, IncludeHealthWarnings, OnHealthRestored, OnApplicationUpdate,
       OnMovieAdded, OnMovieDelete
FROM Notifications
```

Exact SQL for `ReadNamingConfig`:
```sql
-- Sonarr:
SELECT RenameEpisodes, ReplaceIllegalCharacters, ColonReplacementFormat,
       MultiEpisodeStyle, StandardEpisodeFormat, DailyEpisodeFormat,
       AnimeEpisodeFormat, SeriesFolderFormat, SeasonFolderFormat, SpecialsFolderFormat
FROM NamingConfig WHERE Id = 1

-- Radarr:
SELECT RenameMovies, ReplaceIllegalCharacters, ColonReplacementFormat,
       StandardMovieFormat, MovieFolderFormat
FROM NamingConfig WHERE Id = 1
```

#### 2b: Modify `reader_api.go` (Sonnet)

Follow Pattern B (see existing `ReadRootFolders` at line 81). Use anonymous structs for JSON unmarshaling.

API endpoints:
- `GET /api/v3/downloadclient` — returns array with `enable` (G17: NOT `enabled`), `name`, `implementation`, `fields` (NOT flat settings), `priority`, `removeCompletedDownloads`, `removeFailedDownloads`
- `GET /api/v3/indexer` — similar
- `GET /api/v3/notification` — similar; event field names mirror DB columns in camelCase (Sonarr: `onSeriesAdd`/`onSeriesDelete`, Radarr: `onMovieAdded`/`onMovieDelete`). Use `SourceNotification` struct — `json.Unmarshal` fills matching fields, leaves others as zero
- `GET /api/v3/qualityprofile` — returns `upgradeAllowed`, `cutoff`, `items` (nested)
- `GET /api/v3/config/naming` — Sonarr returns episode fields, Radarr returns movie fields; use `SourceNamingConfig` with all fields and `json.Unmarshal` fills what matches

**API Settings JSON note**: The API returns provider settings as a `fields` array (`[{name, value, label, type, ...}]`), NOT as a flat JSON object like the DB. Must convert fields array to flat JSON object. Define a shared type for the fields:
```go
type apiField struct {
    Name  string      `json:"name"`
    Value interface{} `json:"value"`
}

func fieldsToJSON(fields []apiField) json.RawMessage {
    m := make(map[string]interface{})
    for _, f := range fields {
        if f.Value != nil {
            m[f.Name] = f.Value
        }
    }
    b, _ := json.Marshal(m)
    return b
}
```
Use `apiField` in the API reader's anonymous response structs: `Fields []apiField \`json:"fields"\``.

### Phase 3: Service Layer (Opus — complex orchestration) ✅

**Modify `internal/arrimport/service.go`**

1. Add new service interfaces (G4, G5, G6 — use correct names and types):
```go
type DownloadClientImportService interface {
    Create(ctx context.Context, input *downloader.CreateClientInput) (*downloader.DownloadClient, error)
    List(ctx context.Context) ([]*downloader.DownloadClient, error)
}

type IndexerImportService interface {
    Create(ctx context.Context, input *indexer.CreateIndexerInput) (*indexer.IndexerDefinition, error)
    List(ctx context.Context) ([]*indexer.IndexerDefinition, error)
}

type NotificationImportService interface {
    Create(ctx context.Context, input *notification.CreateInput) (*notification.Config, error)
    List(ctx context.Context) ([]notification.Config, error)
}

type QualityProfileImportService interface {
    Create(ctx context.Context, input *quality.CreateProfileInput) (*quality.Profile, error)
    List(ctx context.Context) ([]*quality.Profile, error)
}

type ImportSettingsService interface {
    GetSettings(ctx context.Context) (*importer.ImportSettings, error)
    UpdateSettings(ctx context.Context, settings *importer.ImportSettings) (*importer.ImportSettings, error)
}
```

2. Add fields to `Service` struct and `SetConfigImportServices()` setter (Pattern E).

3. Implement `GetConfigPreview` (Pattern C for mutex):
   - Read all config entities from source reader
   - For each entity: map type, check duplicate by name (case-insensitive), detect redacted creds (G10)
   - Build `ConfigPreview` with initialized slices (G11)

4. Implement `ExecuteConfigImport`:
   - Re-read source entities, filter by selected IDs (G9)
   - For each selected entity, build the `Create*Input` and call the service
   - Collect results into `ConfigImportReport` (initialized slices)

### Phase 4: Handlers and Wiring (parallel: 4a + 4b) ✅

#### 4a: Modify `handlers.go` (Sonnet)

Add routes (Pattern F) and two handlers (Pattern G). `ConfigPreview` is GET (no body). `ConfigImport` is POST with `ConfigImportSelections` body.

#### 4b: Modify `server.go` + `adapters.go` (Sonnet)

**`internal/api/server.go`** — Add near `SetMetadataRefresher` (around line 655), NOT right after `SetSlotsService` (line 472). Reason: `notificationService` is initialized at line 474, AFTER the arrImport block. The late-wiring pattern already exists for `SetMetadataRefresher`:
```go
s.arrImportService.SetConfigImportServices(
    s.downloaderService,
    s.indexerService,
    s.notificationService,
    s.qualityService,
    s.importService,
)
```

Check if any adapters are needed. The existing services should satisfy the interfaces directly since we defined the interfaces to match. If there's a type mismatch (e.g., `indexer.Service.List` returns a different type than the interface expects), add a thin adapter following Pattern D in `adapters.go`.

### Phase 5: Integration Test (Sonnet) ✅

Create `internal/arrimport/config_import_test.go` using `testutil.NewTestDB(t)` for the SlipStream side. For the source side, create a temporary SQLite DB with known test data.

```go
func TestConfigImportEndToEnd(t *testing.T) {
    // 1. Create temporary source DB with test data
    // 2. Create SlipStream test DB
    // 3. Create Service with real services
    // 4. Connect to source
    // 5. GetConfigPreview — verify entities are detected
    // 6. ExecuteConfigImport — verify entities created
    // 7. Verify created entities via List calls
}
```

### Phase 6: Build and Lint ✅

```bash
make build && make lint
```

Fix any issues. Then run:
```bash
go test -v ./internal/arrimport/...
```

---

## Sensitive Data Strategy

| Reader | Passwords/API Keys | Import Behavior |
|---|---|---|
| SQLite (DB) | Plaintext available | Full import, all fields populated, entity enabled |
| API | Redacted to `********` | Preview marks as `incomplete`; import creates with empty creds + `enabled=false`; warning in report |

Detection in service layer: `hasRedactedCredentials(settings)` checks for `"********"` in any JSON string value.

## Duplicate Detection

| Entity | Match By | Preview Status | Import Behavior |
|---|---|---|---|
| Download client | Name (case-insensitive) | `duplicate` | Skip |
| Indexer | Name (case-insensitive) | `duplicate` | Skip |
| Notification | Name (case-insensitive) | `duplicate` | Skip |
| Quality profile | Name (case-insensitive) | `duplicate` | Skip |
| Naming config | — | `different` or `same` | User opts in via `ImportNamingConfig` bool |

---

## Files Summary

| File | Action | Subagent |
|---|---|---|
| `internal/arrimport/config_types.go` | **New** | 1a (Sonnet) |
| `internal/arrimport/config_map.go` | **New** | 1b (Opus) |
| `internal/arrimport/config_map_test.go` | **New** | 1c (Sonnet) |
| `internal/arrimport/reader.go` | **Modify** — add 5 interface methods | 2a (Sonnet) |
| `internal/arrimport/reader_sqlite.go` | **Modify** — implement 5 methods | 2a (Sonnet) |
| `internal/arrimport/reader_api.go` | **Modify** — implement 5 methods | 2b (Sonnet) |
| `internal/arrimport/service.go` | **Modify** — interfaces, fields, preview+import | 3 (Opus) |
| `internal/arrimport/handlers.go` | **Modify** — 2 handlers + routes | 4a (Sonnet) |
| `internal/api/server.go` | **Modify** — wire services | 4b (Sonnet) |
| `internal/api/adapters.go` | **Modify** — adapters if needed | 4b (Sonnet) |
| `internal/arrimport/config_import_test.go` | **New** | 5 (Sonnet) |
