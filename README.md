# tidemark

Detect and display ad markers and timed metadata from live streaming URLs.

Tidemark handles **HLS** playlists and segments, **raw MPEGTS**, **Icecast/SHOUTcast** streams with ICY metadata, and **UDP multicast** inputs from one command with automatic stream detection.

```bash
tidemark <url> [flags]
```

## Install

### Binary

Download a prebuilt binary from [GitHub Releases](https://github.com/keithah/tidemark/releases/latest):

```bash
# macOS (Apple Silicon)
curl -Lo tidemark https://github.com/keithah/tidemark/releases/latest/download/tidemark_darwin_arm64
chmod +x tidemark && sudo mv tidemark /usr/local/bin/

# macOS (Intel)
curl -Lo tidemark https://github.com/keithah/tidemark/releases/latest/download/tidemark_darwin_amd64
chmod +x tidemark && sudo mv tidemark /usr/local/bin/

# Linux (amd64)
curl -Lo tidemark https://github.com/keithah/tidemark/releases/latest/download/tidemark_linux_amd64
chmod +x tidemark && sudo mv tidemark /usr/local/bin/

# Linux (arm64)
curl -Lo tidemark https://github.com/keithah/tidemark/releases/latest/download/tidemark_linux_arm64
chmod +x tidemark && sudo mv tidemark /usr/local/bin/
```

### From Source

```bash
go install github.com/keithah/tidemark/cmd/tidemark@latest
```

## Usage

```bash
# HLS: manifest SCTE-35 tags plus SCTE-35 and timed ID3 inside segments
tidemark https://example.com/live.m3u8

# Icecast/SHOUTcast: ICY metadata
tidemark http://icecast.example.com:8000/stream

# Raw MPEGTS over HTTP
tidemark https://example.com/stream.ts

# UDP multicast MPEGTS
tidemark udp://@239.1.1.1:5000
```

## Output

Default output is a compact table designed for live terminal monitoring:

```text
TIME                  TYPE    CLASS      SEG      FRAME  VALUE
2026-06-16T14:50:00Z  SCTE35  AD_START   42       #EXT-X-CUE-OUT  break=90.023s seg=42
2026-06-16T14:50:02Z  ID3     METADATA   43       TIT2   Song Title
2026-06-16T14:50:08Z  ICY     AD_START            StreamTitle  Promo Spot
```

Use `--json` for newline-delimited JSON on stdout:

```bash
tidemark https://example.com/live.m3u8 --json | jq '.Classification'
```

Example JSON marker:

```json
{"Type":"SCTE35","Classification":"AD_START","Source":"hls_manifest","Tag":"#EXT-X-CUE-OUT","Segment":42,"Timestamp":"2026-06-16T14:50:00Z"}
```

Use `--quiet` for colored one-line summaries, or `--json-out FILE` to write newline-delimited JSON to a file while keeping the selected stdout mode.

Classifications:

- **AD_START** - ad break begins
- **AD_END** - ad break ends
- **METADATA** - metadata was found, but no ad signal was detected

## Flags

| Flag | Description |
|------|-------------|
| `--json` | Emit newline-delimited JSON to stdout. Mutually exclusive with `--quiet`. |
| `--quiet` | Emit summary lines only. Mutually exclusive with `--json`. |
| `--json-out FILE` | Write every marker as newline-delimited JSON to a file alongside normal output. |
| `--timeout N` | Stop after N seconds. Default: 0, which runs until Ctrl+C or SIGTERM. |
| `--filter TYPE` | Show only one marker type: `scte35`, `id3`, or `icy`. |
| `--no-color` | Disable ANSI color codes in human-readable output. |

### Examples

```bash
# Quiet terminal output plus durable JSON capture
tidemark https://example.com/live.m3u8 --quiet --json-out /tmp/markers.ndjson

# Machine-readable JSON piped to jq
tidemark https://example.com/live.m3u8 --json | jq -r '.Type + " " + .Classification'

# Stop after 60 seconds, only SCTE-35 markers
tidemark https://example.com/live.m3u8 --timeout 60 --filter scte35

# Disable color when appending to logs
tidemark http://icecast.example.com:8000/stream --no-color >> markers.log 2>&1
```

## Stream Type Detection

Tidemark probes the URL with ICY metadata enabled and then combines URL patterns with response headers:

| Signal | Detected As |
|--------|-------------|
| URL starts with `udp://` | UDP multicast |
| URL ends in `.m3u8` or contains `/hls/` | HLS |
| URL ends in `.ts` | MPEGTS |
| Response header `icy-metaint` present | ICY |
| Content-Type `application/vnd.apple.mpegurl` or `application/x-mpegurl` | HLS |
| Content-Type `video/mp2t` | MPEGTS |
| Content-Type `audio/mpeg` or `audio/aac` | ICY fallback |

## Ad Classification

### SCTE-35

| Condition | Classification |
|-----------|---------------|
| Splice Insert with OutOfNetworkIndicator=true | AD_START |
| Splice Insert with OutOfNetworkIndicator=false | AD_END |
| Time Signal with segmentation type 0x22, 0x30, or 0x34 | AD_START |
| Time Signal with segmentation type 0x23, 0x31, or 0x35 | AD_END |
| Splice Null or unrecognized SCTE-35 data | METADATA |

### ICY Metadata

| Condition | Classification |
|-----------|---------------|
| StreamTitle contains `ad`, `spot`, `promo`, or `commercial` as a word | AD_START |
| StreamTitle changes to non-ad content after an ad title | AD_END |
| No ad signal found | METADATA |

### ID3 Tags

| Condition | Classification |
|-----------|---------------|
| Any tag value contains `ad`, `spot`, `promo`, or `commercial` as a word | AD_START |
| Any tag value contains `ad_end` or `content_start` | AD_END |
| No ad signal found | METADATA |

## Supported HLS SCTE-35 Tags

- `#EXT-X-SCTE35:CUE=<base64>`
- `#EXT-OATCLS-SCTE35:<base64>`
- `#EXT-X-DATERANGE` with `SCTE35-OUT` or `SCTE35-IN`
- `#EXT-X-CUE-OUT` and `#EXT-X-CUE-IN`

## Supported ID3 Frames

Tidemark emits all structurally valid ID3v2.3 and ID3v2.4 frames. Frames with dedicated parsing include:

- `T***` - standard text frames such as `TIT2`, `TPE1`, `TALB`, `TDRC`, `TCON`, `TRCK`, `TLEN`, and `TPUB`
- `TXXX` - user-defined text
- `COMM` - comments
- `LINK` - linked information
- `WXXX` - user-defined URLs
- `W***` - standard URL frames
- `PRIV` - private data
- `GEOB` - general encapsulated objects

Unknown but structurally valid frames are emitted with best-effort text decoding instead of being dropped.

## Reliability And Limits

- HLS polling uses conditional requests with ETag and Last-Modified validators where servers support them.
- Live HLS segment output is kept in segment order even when marker extraction work runs concurrently.
- HLS manifests are capped at 1 MiB and segment downloads are capped at 32 MiB.
- HLS segment dedupe and URL tracking are bounded so long-running live streams do not grow memory without limit.
- ICY and direct MPEGTS readers use a 30 second idle-read timeout so stalled live connections fail instead of hanging forever.
- ID3 parsing handles tags split across MPEGTS packet boundaries and keeps multiple timed-ID3 events in one segment as separate markers.
- Terminal table output strips control characters from metadata values before printing.

## Architecture

```text
URL
  |
  v
Detector
  |
  +--> ICY reader
  +--> HLS poller and segment decoder
  +--> MPEGTS decoder
  +--> UDP reader
          |
          v
      markers
          |
          v
Classifier (AD_START / AD_END / METADATA)
          |
          v
Output (table, summary, stdout JSON, optional JSON file)
```

Context cancellation from Ctrl+C, SIGTERM, or `--timeout` is propagated through the active reader or poller. File output is flushed and closed explicitly so write, sync, and close errors can be reported.

See [docs/reference.md](docs/reference.md) for maintainer-level behavior notes.

## Development

```bash
make build       # build ./tidemark
make test        # run the cached test suite
make test-fresh  # run the verbose uncached test suite
make vet         # run go vet
make clean       # remove the local binary
```

Useful release checks:

```bash
git diff --check
go test ./...
go vet ./...
```

Releases are tag-driven. Pushing a `v*` tag runs the release workflow, executes tests, and publishes GoReleaser assets to GitHub Releases.

## Dependencies

- [cuei](https://github.com/futzu/cuei) - SCTE-35 decoding for Go

## License

MIT
