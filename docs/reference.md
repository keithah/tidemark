# tidemark - Current Project Reference

This document is the current maintainer reference for tidemark.

## Purpose

`tidemark` is a Go CLI for watching live streaming inputs and emitting ad-marker or metadata events. It supports:

- HLS playlists with SCTE-35 manifest tags.
- HLS media segments containing SCTE-35 or timed ID3.
- Raw MPEGTS over HTTP.
- UDP multicast MPEGTS.
- Icecast and SHOUTcast streams with ICY metadata.

The public module path is:

```text
github.com/keithah/tidemark
```

## CLI

```text
tidemark <url> [flags]

Flags:
  --json            Emit newline-delimited JSON to stdout
  --quiet           Emit human summary lines only
  --json-out FILE   Write all markers as newline-delimited JSON to FILE
  --timeout N       Stop after N seconds; 0 means run until cancelled
  --filter TYPE     Show only scte35, id3, or icy markers
  --no-color        Disable ANSI color in human-readable output
```

`--json` and `--quiet` are mutually exclusive. `--json-out` is independent of stdout mode.

## Output Modes

Default stdout is a compact table:

```text
TIME                  TYPE    CLASS      SEG      FRAME  VALUE
2026-06-16T14:50:00Z  SCTE35  AD_START   42       #EXT-X-CUE-OUT  break=90.023s seg=42
2026-06-16T14:50:02Z  ID3     METADATA   43       TIT2   Song Title
```

`--quiet` prints one summary line per marker. `--json` prints one compact JSON object per marker. `--json-out` always writes the same compact JSON shape as newline-delimited JSON.

Canonical marker JSON:

```json
{
  "Type": "SCTE35",
  "Classification": "AD_START",
  "Source": "hls_manifest",
  "Tag": "#EXT-X-CUE-OUT",
  "PTS": 38113.135,
  "Segment": 42,
  "RawBase64": "...",
  "Tags": {"TIT2": "Song Title"},
  "Fields": {"StreamTitle": "Promo Spot"},
  "Timestamp": "2026-06-16T14:50:00Z"
}
```

Empty fields are omitted. Internal SCTE-35 details are used for classification and display, but are not serialized directly.

## Detection

Startup detection combines URL patterns and response metadata:

| Signal | Detected stream |
|--------|-----------------|
| `udp://` URL | UDP |
| `.m3u8` URL or `/hls/` path | HLS |
| `.ts` URL | MPEGTS |
| `icy-metaint` response header | ICY |
| MPEGURL content type | HLS |
| `video/mp2t` content type | MPEGTS |
| `audio/mpeg` or `audio/aac` content type | ICY fallback |

HTTP probes send `Icy-Metadata: 1` so ICY streams can identify themselves.

## Classification

Classification values are:

- `AD_START`
- `AD_END`
- `METADATA`

`METADATA` means a marker or tag was found, but no ad boundary signal was identified.

SCTE-35 rules:

| Input | Classification |
|-------|----------------|
| Splice Insert with OutOfNetworkIndicator=true | AD_START |
| Splice Insert with OutOfNetworkIndicator=false | AD_END |
| Time Signal segmentation type 0x22, 0x30, or 0x34 | AD_START |
| Time Signal segmentation type 0x23, 0x31, or 0x35 | AD_END |
| Anything else | METADATA |

ICY and ID3 rules:

- `ad`, `spot`, `promo`, or `commercial` as a word produces `AD_START`.
- `ad_end` or `content_start` in ID3 produces `AD_END`.
- ICY emits `AD_END` when the stream title changes from ad-like content back to non-ad content.

## HLS Behavior

The HLS path handles both manifests and media segments.

- Master playlists are resolved to a media playlist.
- Live playlists are polled at an interval derived from target duration.
- Conditional GETs use ETag and Last-Modified validators when available.
- HTTP 304 responses are treated as no-change polls.
- Permanent 4xx manifest errors fail fast, except 429.
- Segment marker output is kept in segment order.
- Segment dedupe and URL tracking are bounded for long-running streams.

Supported manifest marker tags:

- `#EXT-X-SCTE35:CUE=<base64>`
- `#EXT-OATCLS-SCTE35:<base64>`
- `#EXT-X-DATERANGE` with `SCTE35-OUT` or `SCTE35-IN`
- `#EXT-X-CUE-OUT`
- `#EXT-X-CUE-IN`

## ID3 Behavior

ID3 parsing supports ID3v2.3 and ID3v2.4. It parses standard text frames, user text frames, comments, links, URL frames, private data, general encapsulated objects, and best-effort text from other structurally valid frames.

`ParseFromMPEGTS` returns grouped tag events:

```go
func ParseFromMPEGTS(data []byte) ([][]Tag, error)
```

Each inner slice is one timed-ID3 event. This matters for HLS segments that contain multiple PES payloads with duplicate frame IDs; the segment decoder emits one marker per event instead of flattening the segment into one map.

## Resource Limits And Failure Modes

- HLS manifest reads are capped at 1 MiB.
- HLS segment reads are capped at 32 MiB.
- Oversized ID3 tags are skipped instead of allocating unbounded memory.
- ICY and direct MPEGTS streams use a 30 second idle-read timeout.
- MPEGTS decoder panics are recovered and returned as errors.
- UDP reads close the socket on cancellation so shutdown does not block.
- JSON file output flushes, syncs, and closes explicitly, and reports those errors.
- Table output sanitizes control characters from metadata values.

## Development

```bash
make build       # build ./tidemark
make test        # cached go test ./...
make test-fresh  # verbose uncached go test ./... -count=1
make vet         # go vet ./...
make clean       # remove ./tidemark
```

Use these checks before committing:

```bash
git diff --check
go test ./...
go vet ./...
```

## Release

Releases are created from `v*` tags. The release workflow runs tests and GoReleaser, then publishes assets under the canonical GitHub repository `keithah/tidemark`.
