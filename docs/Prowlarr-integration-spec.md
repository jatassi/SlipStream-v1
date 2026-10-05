# Prowlarr Integration Specification

## 1. Background

1.1: SlipStream provides indexer management functionality comparable to Prowlarr
1.2: Users migrating from the *arr ecosystem may prefer to continue using Prowlarr for indexer management
1.3: These users would use SlipStream solely for media management

## 2. Feature Overview

The indexer management system is configurable with 2 mutually exclusive modes:

### 2.1. SlipStream Mode (Default)
2.1.1: Labeled as **"Experimental"** in the UI
2.1.2: Default for new installations
2.1.3: Current application behavior completely unchanged
2.1.4: SlipStream manages the complete indexer flow via Cardigann definitions

### 2.2. Prowlarr Mode
2.2.1: No label (unlabeled option)
2.2.2: Disables internal indexer management functions
2.2.3: Integrates with Prowlarr using **Aggregated Mode** only
2.2.4: All searches route through Prowlarr's aggregated endpoint (`/api`)

## 3. Integration Architecture

### 3.1. Aggregated Mode

SlipStream uses Prowlarr's aggregated endpoint exclusively:
3.1.1: **Search Endpoint**: `http(s)://<prowlarr-host>:<port>/api`
3.1.2: **Download Endpoint**: `http(s)://<prowlarr-host>:<port>/<indexer_id>/api?t=get&id=<guid>` or `/<indexer_id>/download`
3.1.3: Prowlarr queries all enabled indexers in parallel and returns unified results
3.1.4: No individual indexer configurations pushed to SlipStream

### 3.2. Why Aggregated Mode Only

3.2.1: Simpler configuration (single endpoint vs multiple indexers)
3.2.2: Changes in Prowlarr are instant (no sync required)
3.2.3: Prowlarr handles indexer authentication, rate limiting, and health
3.2.4: Reduces maintenance overhead for SlipStream

## 4. Configuration

### 4.1. Location

4.1.1: **UI**: Mode toggle at top of Indexers page with conditional content
4.1.2: **Storage**: Dedicated `prowlarr_config` database table
4.1.3: **API**: `/api/v1/indexers/prowlarr`

### 4.2. Configuration Fields

4.2.1: `enabled` - boolean - false - Whether Prowlarr mode is active
4.2.2: `url` - string - - - Prowlarr base URL (e.g., `http://localhost:9696`)
4.2.3: `api_key` - string - - - Prowlarr API key (displayed in plain text for easy copying)
4.2.4: `movie_categories` - int[] - [2000,2010,2020,2030,2040,2045,2050,2060] - Category IDs for movie searches
4.2.5: `tv_categories` - int[] - [5000,5010,5020,5030,5040,5045,5050,5060,5070,5080] - Category IDs for TV searches
4.2.6: `timeout` - int - 90 - Request timeout in seconds
4.2.7: `skip_ssl_verify` - boolean - true - Skip SSL certificate verification (permissive by default for home setups)

### 4.3. Validation

4.3.1: **URL**: Must be valid URL format
4.3.2: **API Key**: Required, non-empty
4.3.3: **Save Behavior**: Block save on validation failure - successful connection test required before saving

## 5. Mode Switching Behavior

### 5.1. Switching to Prowlarr Mode

5.1.1: Internal indexers remain in database but are hidden/disabled
5.1.2: Indexer management UI replaced with Prowlarr configuration
5.1.3: Auto-search schedules continue unchanged (just route through Prowlarr)
5.1.4: All search/grab history preserved

### 5.2. Switching to SlipStream Mode

5.2.1: Internal indexers re-enabled and visible
5.2.2: Prowlarr configuration preserved but inactive
5.2.3: Schedules continue unchanged
5.2.4: History preserved

### 5.3. Developer Mode Override

5.3.1: When Developer Mode is enabled, SlipStream mode is forced regardless of user setting
5.3.2: This ensures the mock indexer is available for testing without requiring a real Prowlarr instance

## 6. User Interface

### 6.1. Indexers Page - Mode Selection

```
┌─────────────────────────────────────────────────────────────┐
│ Indexer Management                                          │
│                                                             │
│ ○ SlipStream (Experimental)  ● Prowlarr                    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### 6.2. Indexers Page - Prowlarr Mode Content

```
┌─────────────────────────────────────────────────────────────┐
│ Prowlarr Connection                              [Refresh]  │
├─────────────────────────────────────────────────────────────┤
│ URL:        [http://localhost:9696              ]          │
│ API Key:    [abc123...                          ]          │
│ Timeout:    [90] seconds                                    │
│ [ ] Verify SSL Certificate                                  │
│                                                             │
│ Movie Categories: [Configure...]                            │
│ TV Categories:    [Configure...]                            │
│                                                             │
│            [Test Connection]  [Save]                        │
├─────────────────────────────────────────────────────────────┤
│ Status: ● Connected (Last checked: 2 min ago)              │
├─────────────────────────────────────────────────────────────┤
│ Indexers from Prowlarr                                      │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Name          │ Protocol │ Status  │ Capabilities      │ │
│ ├───────────────┼──────────┼─────────┼───────────────────┤ │
│ │ 1337x         │ Torrent  │ Healthy │ Search, TV, Movie │ │
│ │ RARBG         │ Torrent  │ Healthy │ Search, TV, Movie │ │
│ │ NZBGeek       │ Usenet   │ Healthy │ Search, TV, Movie │ │
│ │ ...           │          │         │                   │ │
│ └─────────────────────────────────────────────────────────┘ │
│                                        (Read-only)          │
└─────────────────────────────────────────────────────────────┘
```

### 6.3. Indexer List Display

For each indexer fetched from Prowlarr, display:
6.3.1: **Name**: Indexer name
6.3.2: **Protocol**: Torrent or Usenet
6.3.3: **Status**: Healthy / Warning / Disabled (from Prowlarr's indexer status)
6.3.4: **Capabilities**: Supported search types (Search, TV, Movie) and category summary

## 7. Search Behavior

### 7.1. Search Types

Use Prowlarr's capabilities to determine search type:

| SlipStream Context | Prowlarr Search Type | Parameters |
|-------------------|---------------------|------------|
| Movie search | `t=movie` if supported, else `t=search` | `tmdbid`, `imdbid`, `q`, `cat=<movie_categories>` |
| TV search | `t=tvsearch` if supported, else `t=search` | `tvdbid`, `tmdbid`, `q`, `season`, `ep`, `cat=<tv_categories>` |
| General search | `t=search` | `q`, `cat` |

### 7.2. Category Auto-Filtering

7.2.1: Movie searches: Automatically apply configured movie categories (2000s)
7.2.2: TV searches: Automatically apply configured TV categories (5000s)

### 7.3. Search Result Processing

7.3.1: Parse Torznab XML response
7.3.2: Extract all releases with extended attributes (`extended=1`)
7.3.3: Apply SlipStream's full scoring algorithm:
   7.3.3.1: Quality profile matching
   7.3.3.2: Seeder count (logarithmic scale)
   7.3.3.3: Freeleech bonus
   7.3.3.4: Age scoring
   7.3.3.5: Language detection from title
7.3.4: Trust Prowlarr's deduplication (no additional dedup)
7.3.5: No pagination - fetch all results in single request

### 7.4. Search Result Caching

7.4.1: Cache search results for 5 minutes
7.4.2: Key: search criteria hash
7.4.3: Cleared on manual refresh

### 7.5. Torznab Attributes Used

| Attribute | Usage |
|-----------|-------|
| `title` | Display, quality parsing |
| `guid` | Unique identifier for grab |
| `link` / `enclosure` | Download URL |
| `size` | File size display |
| `pubDate` | Release age scoring |
| `seeders` | Health scoring |
| `peers` / `leechers` | Health display |
| `category` | Category matching |
| `downloadvolumefactor` | Freeleech detection (0 = freeleech) |
| `uploadvolumefactor` | Upload multiplier display |
| `minimumratio` | Ratio requirement display |
| `minimumseedtime` | Seed time requirement display |
| `indexer` | Source indexer name for display |

### 7.6. Search Results Display

7.6.1: Show individual indexer name from Torznab `indexer` attribute (not "Prowlarr")
7.6.2: Display all standard release information
7.6.3: Show freeleech/ratio requirements when available

## 8. Grab (Download) Behavior

### 8.1. Grab Flow

8.1.1: User selects release to grab
8.1.2: SlipStream downloads torrent/NZB via Prowlarr's download endpoint
   8.1.2.1: URL: `/{indexer_id}/api?t=get&id={guid}` or `/{indexer_id}/download?link={url}`
   8.1.2.2: This ensures Prowlarr handles indexer authentication
8.1.3: Select download client based on protocol (Torrent → torrent client, Usenet → usenet client)
8.1.4: Send to download client
8.1.5: Record in grab history with indexer name
8.1.6: Broadcast WebSocket event

### 8.2. Grab Failure Handling

8.2.1: Retry once automatically after short delay
8.2.2: If retry fails, show error message to user
8.2.3: Let user try alternative releases manually

### 8.3. Download Client Selection

8.3.1: Automatic protocol-based selection (no user configuration needed)
8.3.2: Torrent releases → first available torrent client
8.3.3: Usenet releases → first available usenet client

## 9. Health Monitoring

### 9.1. Periodic Checks

9.1.1: **Interval**: Every 15 minutes
9.1.2: **Also triggers on**: Page load, manual refresh button click
9.1.3: **Check type**: Capabilities request (`t=caps`)

### 9.2. Capabilities Check

9.2.1: Verify Prowlarr is reachable
9.2.2: Fetch supported categories
9.2.3: Fetch supported search types
9.2.4: Cache for use in searches

### 9.3. Indexer List Refresh

9.3.1: Fetch Prowlarr's indexer list via `/api/v1/indexer`
9.3.2: Update displayed indexer statuses
9.3.3: Every 15 minutes + on page load + manual refresh

### 9.4. Health Status After Search

9.4.1: After each search, check Prowlarr's indexer status API
9.4.2: If any indexers are failing/disabled, show warning to user
9.4.3: Warning: "Some indexers may have failed. Check Prowlarr for details."

### 9.5. Health Integration

9.5.1: Prowlarr connection status integrated with SlipStream's health monitoring system
9.5.2: Toast notifications for connection errors
9.5.3: Status indicator on Indexers page
9.5.4: Health check failures recorded in health service

## 10. Error Handling

### 10.1. Connection Failures

10.1.1: **Behavior**: Fail gracefully with error message
10.1.2: **Display**: Toast notification + update status indicator
10.1.3: **No automatic retry or queuing**

### 10.2. Rate Limiting

10.2.1: **Strategy**: Adaptive rate limiting
10.2.2: **Initial**: No rate limiting applied
10.2.3: **On rate limit error (429)**: Back off and reduce request frequency
10.2.4: **Recovery**: Gradually increase rate after successful requests

### 10.3. Search Errors

10.3.1: Show error toast to user
10.3.2: Return empty results
10.3.3: Log error details (verbose in dev mode)

## 11. Logging

| Environment | Log Level |
|-------------|-----------|
| Local development (`go run`) | Verbose debug (full request/response) |
| Developer Mode enabled | Verbose debug |
| Production | Standard (errors + important events) |

### 11.1. Logged Events

11.1.1: Connection attempts (success/failure)
11.1.2: Search requests (query, categories, result count)
11.1.3: Grab requests (release, indexer, success/failure)
11.1.4: Rate limit hits
11.1.5: Configuration changes

## 12. API Endpoints

### 12.1. Prowlarr Configuration

12.1.1: `GET /api/v1/indexers/prowlarr` - Get Prowlarr configuration
12.1.2: `PUT /api/v1/indexers/prowlarr` - Update Prowlarr configuration
12.1.3: `POST /api/v1/indexers/prowlarr/test` - Test Prowlarr connection
12.1.4: `GET /api/v1/indexers/prowlarr/indexers` - Get read-only indexer list from Prowlarr
12.1.5: `GET /api/v1/indexers/prowlarr/status` - Get Prowlarr connection status

### 12.2. Indexer Mode

12.2.1: `GET /api/v1/indexers/mode` - Get current indexer mode
12.2.2: `PUT /api/v1/indexers/mode` - Set indexer mode (slipstream | prowlarr)

### 12.3. Existing Endpoints (Mode-Aware)

These endpoints behave differently based on active mode:
12.3.1: `GET /api/v1/indexers` - Returns internal indexers (SlipStream) or empty (Prowlarr)
12.3.2: `GET /api/v1/search/*` - Routes through internal indexers (SlipStream) or Prowlarr (Prowlarr mode)
12.3.3: `POST /api/v1/search/grab` - Uses internal grab (SlipStream) or Prowlarr grab (Prowlarr mode)

## 13. Database Schema

### 13.1. New Table: `prowlarr_config`

13.1.1: Table name: `prowlarr_config`
13.1.2: Database fields:
   13.1.2.1: `id` - INTEGER PRIMARY KEY
   13.1.2.2: `enabled` - BOOLEAN NOT NULL DEFAULT false
   13.1.2.3: `url` - TEXT NOT NULL DEFAULT ''
   13.1.2.4: `api_key` - TEXT NOT NULL DEFAULT ''
   13.1.2.5: `movie_categories` - TEXT NOT NULL DEFAULT '[]' (JSON array)
   13.1.2.6: `tv_categories` - TEXT NOT NULL DEFAULT '[]' (JSON array)
   13.1.2.7: `timeout` - INTEGER NOT NULL DEFAULT 90
   13.1.2.8: `skip_ssl_verify` - BOOLEAN NOT NULL DEFAULT true
   13.1.2.9: `capabilities` - TEXT DEFAULT NULL (Cached capabilities JSON)
   13.1.2.10: `capabilities_updated_at` - TIMESTAMP
   13.1.2.11: `created_at` - TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
   13.1.2.12: `updated_at` - TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP

```sql
CREATE TABLE prowlarr_config (
    id INTEGER PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT false,
    url TEXT NOT NULL DEFAULT '',
    api_key TEXT NOT NULL DEFAULT '',
    movie_categories TEXT NOT NULL DEFAULT '[]',  -- JSON array
    tv_categories TEXT NOT NULL DEFAULT '[]',     -- JSON array
    timeout INTEGER NOT NULL DEFAULT 90,
    skip_ssl_verify BOOLEAN NOT NULL DEFAULT true,
    capabilities TEXT DEFAULT NULL,               -- Cached capabilities JSON
    capabilities_updated_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

## 14. WebSocket Events

Same events broadcast regardless of indexer mode:

14.1: `search:started` - Payload: `{ mediaType, mediaId, source }`
14.2: `search:completed` - Payload: `{ mediaType, mediaId, resultCount, source }`
14.3: `grab:started` - Payload: `{ release, mediaType, mediaId }`
14.4: `grab:completed` - Payload: `{ release, mediaType, mediaId, success }`
14.5: `indexer:status` - Payload: `{ indexerId, status }`

## 15. Notifications

15.1: External notifications (Discord, email, etc.) use identical format regardless of indexer mode
15.2: No indication of whether release came from Prowlarr

## 16. Protocol Support

Both Torrent (Torznab) and Usenet (Newznab) protocols fully supported:
16.1: Search results from both protocols displayed together
16.2: Download client auto-selected based on release protocol
16.3: Torrent-specific attributes (seeders, freeleech) only shown for torrents

## 17. Auto-Search Integration

Auto-search (scheduled missing/upgrade searches) works unchanged in Prowlarr mode:
17.1: Same scheduling logic
17.2: Same quality profile evaluation
17.3: Just routes searches through Prowlarr instead of internal indexers

## 18. Non-Functional Requirements

### 18.1. Security
18.1.1: HTTP and HTTPS both supported
18.1.2: SSL verification permissive by default (home setups often use self-signed certs)
18.1.3: API key stored in plain text in database (consistent with *arr applications)

### 18.2. Performance
18.2.1: 90-second timeout for Prowlarr requests (accommodates slow indexer aggregation)
18.2.2: 5-minute search result caching
18.2.3: 15-minute capability/indexer list refresh

### 18.3. Compatibility
18.3.1: Target Prowlarr API v1 (current stable version)
18.3.2: Standard Torznab/Newznab protocol compliance

## 19. Reference Documentation

19.1: See `Prowlarr-other-arr-integration.md` for detailed Torznab/Newznab protocol specification and Prowlarr API documentation

## 20. Relevant Repositories

20.1: Prowlarr repo cloned locally for reference: `~/Developer/Prowlarr`
20.2: Radarr repo cloned locally for reference: `~/Developer/Radarr`
