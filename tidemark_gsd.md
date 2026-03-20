# tidemark — GSD Spec

## Overview

`tidemark` is a Go CLI that detects and displays ad markers from any streaming URL — HLS (SCTE-35 manifest tags + ID3 in segments), raw MPEGTS (SCTE-35 via cuei), and Icecast/SHOUTcast streams (ICY metadata). A single command handles all stream types via auto-detection.

```
tidemark <url> [flags]
```

## Repositories / Dependencies

- **SCTE-35 decoding**: `github.com/futzu/cuei` — Go lib, handles MPEGTS, Base64, Hex, Bytes
- **ICY reference**: `tunein/streaming-common` icymetadata.go — adapt directly (not imported as module)
- **No other external frameworks** — stdlib net/http, bufio, encoding/json, flag

---

## CLI Interface

```
tidemark <url> [flags]

Flags:
  --json            Machine-readable JSON output to stdout (suppresses human summary)
  --json-out FILE   Write all marker JSON to FILE (in addition to normal stdout output)
  --quiet           Summary lines only, suppress JSON blocks from stdout
  --timeout N       Stop after N seconds (default: 0 = run until Ctrl+C)
  --filter TYPE     Only show markers of type: scte35 | id3 | icy
  --no-color        Disable ANSI color output
```

### Output format (default — JSON block first, summary line after)

```
{
    "Type": "SCTE35",
    "Classification": "AD_START",
    "Source": "hls_manifest",
    "Tag": "#EXT-X-SCTE35",
    "PTS": 38113.135577,
    "Segment": 42,
    "Command": {
        "Name": "Splice Insert",
        "SpliceEventID": "0x5d",
        "OutOfNetworkIndicator": true,
        "BreakDuration": 90.023,
        "PTS": 38113.135577
    },
    ...
}
▶ [SCTE35] AD_START  Splice Insert  break=90.02s  pts=38113.135  seg=42

{
    "Type": "ICY",
    "Classification": "UNKNOWN",
    "Source": "icy_stream",
    "Fields": {
        "StreamTitle": "Pandora Spot | Artist: Tide",
        "icy-genre": "Pop",
        "icy-br": "128"
    }
}
▶ [ICY]    UNKNOWN   StreamTitle=Pandora Spot | Artist: Tide

{
    "Type": "ID3",
    "Classification": "AD_START",
    "Source": "hls_segment",
    "Segment": 7,
    "Tags": {
        "TIT2": "Ad Break",
        "TXXX": "ad_id:abc123"
    }
}
▶ [ID3]    AD_START  TIT2=Ad Break  TXXX=ad_id:abc123
```

With `--quiet`: only the `▶` summary lines are printed.
With `--json`: only the JSON blocks are printed (no `▶` lines).
With `--json-out FILE`: JSON blocks are also written to file (one JSON object per line, newline-delimited).

---

## Stream Type Detection

Auto-detect on startup by probing the URL:

| Signal | Detected As |
|--------|-------------|
| URL ends in `.m3u8` | HLS |
| Response `Content-Type: application/vnd.apple.mpegurl` or `application/x-mpegurl` | HLS |
| Response header `icy-metaint` present | ICY |
| Response `Content-Type: video/mp2t` or `.ts` URL | MPEGTS |
| Response `Content-Type: audio/mpeg` or `audio/aac` without ICY | ICY fallback probe |

Detection sends an initial HEAD or GET with `Icy-Metadata: 1` header so ICY streams self-identify.

---

## SCTE-35 Classification Logic

Derived from cuei's decoded `Command` and `OutOfNetworkIndicator`:

| Condition | Classification |
|-----------|---------------|
| `SpliceInsert` + `OutOfNetworkIndicator=true` | `AD_START` |
| `SpliceInsert` + `OutOfNetworkIndicator=false` | `AD_END` |
| `TimeSignal` + segmentation descriptor type `0x22`/`0x30`/`0x34` | `AD_START` |
| `TimeSignal` + segmentation descriptor type `0x23`/`0x31`/`0x35` | `AD_END` |
| `SpliceNull` or unrecognized | `UNKNOWN` |

---

## ICY Metadata Classification Logic

Heuristic on `StreamTitle` field:

| Condition | Classification |
|-----------|---------------|
| Contains known ad-signal keywords: `"spot"`, `"ad"`, `"promo"`, `"commercial"` (case-insensitive) | `AD_START` |
| StreamTitle changes to non-ad content after ad keyword seen | `AD_END` |
| No match | `UNKNOWN` |

---

## ID3 Tag Classification Logic

| Condition | Classification |
|-----------|---------------|
| `TXXX` or `TIT2` contains `"ad"`, `"spot"`, `"promo"` (case-insensitive) | `AD_START` |
| `TXXX` contains `"ad_end"`, `"content_start"` | `AD_END` |
| No match | `UNKNOWN` |

---

## Project Layout

```
tidemark/
├── cmd/
│   └── tidemark/
│       └── main.go          # CLI entrypoint, flag parsing, URL routing
├── internal/
│   ├── detector/
│   │   └── detector.go      # Stream type detection
│   ├── hls/
│   │   ├── poller.go        # Manifest polling loop (live + VOD)
│   │   ├── tags.go          # HLS tag parsers (EXT-X-SCTE35, OATCLS, DATERANGE)
│   │   └── segments.go      # Segment downloader + ID3/MPEGTS dispatch
│   ├── scte35/
│   │   └── scte35.go        # Thin wrapper: cuei decode → internal Marker struct
│   ├── id3/
│   │   └── id3.go           # ID3 tag extractor from raw segment bytes
│   ├── icy/
│   │   └── icy.go           # ICY stream reader (adapted from icymetadata.go)
│   ├── classifier/
│   │   └── classifier.go    # AD_START / AD_END / UNKNOWN logic
│   └── output/
│       ├── printer.go       # Stdout writer (JSON block + summary line)
│       └── jsonout.go       # --json-out file writer
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## Marker Struct (internal canonical type)

```go
// internal/marker/marker.go
type MarkerType string
const (
    TypeSCTE35 MarkerType = "SCTE35"
    TypeID3    MarkerType = "ID3"
    TypeICY    MarkerType = "ICY"
)

type Classification string
const (
    AdStart  Classification = "AD_START"
    AdEnd    Classification = "AD_END"
    Unknown  Classification = "UNKNOWN"
)

type Marker struct {
    Type           MarkerType     `json:"Type"`
    Classification Classification `json:"Classification"`
    Source         string         `json:"Source"`         // "hls_manifest", "hls_segment", "mpegts", "icy_stream"
    Tag            string         `json:"Tag,omitempty"`  // e.g. "#EXT-X-SCTE35"
    PTS            float64        `json:"PTS,omitempty"`
    Segment        int            `json:"Segment,omitempty"`
    RawB64         string         `json:"RawBase64,omitempty"`
    Command        interface{}    `json:"Command,omitempty"`   // cuei command struct
    Descriptors    interface{}    `json:"Descriptors,omitempty"`
    Tags           map[string]string `json:"Tags,omitempty"`  // ID3 tags
    Fields         map[string]string `json:"Fields,omitempty"` // ICY fields
    Timestamp      time.Time      `json:"Timestamp"`
}
```

---

## Milestones

---

### M1 — Project scaffold + stream detection + ICY

**Goal**: Binary compiles, detects ICY streams, prints ICY markers.

#### Phase 1.1 — Scaffold
- Init Go module `github.com/yourname/tidemark` (or local path)
- Create directory structure per layout above
- `go.mod` with `github.com/futzu/cuei` dependency
- `Makefile` with `build`, `install`, `clean` targets
- Stub `main.go` that prints usage and exits

**Exit criteria**: `go build ./cmd/tidemark` succeeds; `tidemark` prints usage.

#### Phase 1.2 — Stream detector
- Implement `internal/detector/detector.go`
- `Detect(url string) (StreamType, error)` — sends GET with `Icy-Metadata: 1`, reads response headers
- Returns `StreamTypeHLS`, `StreamTypeMPEGTS`, `StreamTypeICY`, `StreamTypeUnknown`
- Log detected type to stderr on startup

**Exit criteria**: `tidemark https://some-icecast-url` logs `[tidemark] detected: ICY`.

#### Phase 1.3 — ICY reader
- Implement `internal/icy/icy.go` adapted from `tunein/streaming-common` icymetadata.go
- Open persistent HTTP connection with `Icy-Metadata: 1` header
- Parse `icy-metaint` from response headers
- Read audio data in metaint-sized chunks; extract inline ICY metadata blocks
- Parse `StreamTitle=...;icy-genre=...` etc. into `map[string]string`
- Emit `Marker{Type: TypeICY, Fields: ...}` on each metadata change

#### Phase 1.4 — Classifier (ICY rules)
- Implement `internal/classifier/classifier.go`
- `Classify(m *Marker) Classification` — ICY heuristic rules from spec above
- Wire into ICY reader output

#### Phase 1.5 — Output printer (basic)
- Implement `internal/output/printer.go`
- `Print(m *Marker, cfg OutputConfig)` — writes JSON block then `▶` summary line
- Handle `--quiet` (summary only), `--json` (JSON only), `--no-color`
- Color scheme: AD_START = green, AD_END = yellow, UNKNOWN = gray

**Exit criteria**: `tidemark <icecast-url>` prints colored ICY markers continuously until Ctrl+C.

---

### M2 — HLS manifest polling + SCTE-35 tag extraction

**Goal**: Parse SCTE-35 from HLS manifest tags without touching segments.

#### Phase 2.1 — HLS manifest poller
- Implement `internal/hls/poller.go`
- Poll manifest URL, track `EXT-X-MEDIA-SEQUENCE` to avoid re-processing segments
- Handle both master (pick first rendition) and media playlists
- Respect `#EXT-X-TARGETDURATION` for poll interval (poll at targetDuration/2)
- Stop on `#EXT-X-ENDLIST` or `--timeout`

#### Phase 2.2 — HLS tag parsers
- Implement `internal/hls/tags.go`
- Parse `#EXT-X-SCTE35:CUE=<base64>` → extract base64 payload
- Parse `#EXT-OATCLS-SCTE35:<base64>` → extract base64 payload
- Parse `#EXT-X-DATERANGE` → extract `SCTE35-OUT=`, `SCTE35-IN=`, `START-DATE=`
- Emit raw base64 strings to scte35 decoder

#### Phase 2.3 — SCTE-35 wrapper
- Implement `internal/scte35/scte35.go`
- `Decode(b64 string, source string, seg int) (*Marker, error)`
- Uses `cuei.NewCue()` + `cue.Decode(b64)`
- Maps cuei output → internal `Marker` struct (Command, Descriptors, PTS, RawB64)
- Wire classifier for AD_START/AD_END

**Exit criteria**: `tidemark https://example.com/stream.m3u8` prints SCTE-35 markers from manifest tags in real time.

---

### M3 — MPEGTS segment decoding (cuei stream)

**Goal**: Decode SCTE-35 from MPEGTS — both direct `.ts` URLs and HLS segments.

#### Phase 3.1 — Direct MPEGTS stream
- Implement `internal/hls/segments.go` (MPEGTS path)
- For `StreamTypeMPEGTS`: open HTTP stream, read in 188-byte multiples
- Feed to `cuei.NewStream().DecodeBytes(buf)` in a loop
- Map returned `[]*cuei.Cue` → `[]*Marker` via scte35 wrapper

#### Phase 3.2 — HLS MPEGTS segments
- Extend HLS poller: for each new segment URI, download and dispatch to MPEGTS decoder
- Segment number tracked from `EXT-X-MEDIA-SEQUENCE`
- Avoid re-processing: maintain set of seen segment URIs

#### Phase 3.3 — Multicast support (bonus)
- If URL scheme is `udp://` → open UDP multicast socket
- Read 1316-byte datagrams (188×7), feed to cuei DecodeBytes

**Exit criteria**: `tidemark https://example.com/stream.m3u8` decodes SCTE-35 from both manifest tags AND segment MPEGTS. `tidemark https://example.com/video.ts` works for raw MPEGTS.

---

### M4 — ID3 tag extraction from HLS segments

**Goal**: Extract ID3 metadata from AAC and MPEGTS HLS segments.

#### Phase 4.1 — ID3 parser
- Implement `internal/id3/id3.go`
- Detect ID3v2 frame header (`ID3` magic bytes at offset 0)
- Parse standard text frames: `TIT2`, `TIT3`, `TXXX`, `PRIV`, `GEOB`
- For MPEGTS segments: scan PES packets for PID carrying ID3 (typically PID 0x0004 or signaled in PMT as stream type 0x15)
- Return `map[string]string` of tag name → value

#### Phase 4.2 — Wire into HLS segment pipeline
- In `segments.go`: after downloading segment, run ID3 scan in parallel with MPEGTS decode
- Emit `Marker{Type: TypeID3, Tags: ...}` for each ID3 frame found
- Wire classifier for AD_START/AD_END

**Exit criteria**: `tidemark <hls-url-with-id3>` prints ID3 markers alongside SCTE-35 markers.

---

### M5 — Output polish + flags + README

**Goal**: All flags wired, `--json-out` file writing, graceful shutdown, README.

#### Phase 5.1 — Remaining flags
- Wire `--timeout N`: context with deadline, cancel all goroutines cleanly
- Wire `--filter TYPE`: marker pipeline filters by type before printing
- Wire `--json-out FILE`: implement `internal/output/jsonout.go`
  - Open file on start, write one JSON object per line (NDJSON)
  - Flush and close on shutdown

#### Phase 5.2 — Graceful shutdown
- Trap `SIGINT`/`SIGTERM`
- Cancel context, drain in-flight markers, close `--json-out` file cleanly
- Print final `[tidemark] stopped. N markers detected.` to stderr

#### Phase 5.3 — Startup header
- On launch, print to stderr:
  ```
  tidemark v0.1.0
  url:    https://...
  type:   HLS
  filter: all
  output: stdout + json-out:markers.json
  ─────────────────────────────────────────
  ```

#### Phase 5.4 — README
- Usage examples for all three stream types
- Sample output screenshots
- `go install` instructions
- Dependency attribution (cuei, icymetadata.go)

**Exit criteria**: All flags work. `--json-out` produces valid NDJSON. Ctrl+C exits cleanly with summary count. README covers all use cases.

---

## Example Invocations

```bash
# HLS stream — all markers
tidemark https://example.com/live.m3u8

# Icecast stream — ICY only
tidemark http://icecast.example.com:8000/stream --filter icy

# Raw MPEGTS
tidemark https://example.com/stream.ts

# Quiet mode, save JSON to file
tidemark https://example.com/live.m3u8 --quiet --json-out /tmp/markers.ndjson

# Stop after 60 seconds
tidemark https://example.com/live.m3u8 --timeout 60

# Machine-readable JSON to stdout (for piping to jq)
tidemark https://example.com/live.m3u8 --json | jq '.Classification'
```

---

## Notes for Claude Code

- Go version: 1.21+
- `cuei` import path: `github.com/futzu/cuei`
- ICY source: adapt `icymetadata.go` from tunein/streaming-common inline (do not import as module — copy and adapt into `internal/icy/icy.go`)
- All goroutines must respect a `context.Context` passed from main
- No global state — pass config structs explicitly
- Marker channel pattern: each detector (hls, mpegts, icy) sends `*Marker` to a shared `chan *Marker`; output package reads from the channel
- Color output via `\033[...m` escape codes directly (no color library dependency)
- Tests: unit tests for classifier logic, tag parsers, ID3 parser; integration test stubs with sample `.ts` and `.m3u8` fixtures
