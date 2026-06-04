# tidemark

Detect and display ad markers from any streaming URL in real time.

Tidemark handles **HLS** (SCTE-35 manifest tags + ID3 in segments), **raw MPEGTS** (SCTE-35 via cuei), **Icecast/SHOUTcast** streams (ICY metadata), and **UDP multicast** — all from a single command with auto-detection.

```
tidemark <url> [flags]
```

## Install

### Binary (recommended)

Download from [GitHub Releases](https://github.com/keithah/tidemark/releases/latest):

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

### From source

```bash
go install github.com/keithah/tidemark/cmd/tidemark@latest
```

## Usage

### HLS stream — detect SCTE-35 from manifest tags and segments

```bash
tidemark https://example.com/live.m3u8
```

### Icecast/SHOUTcast stream — detect ad markers from ICY metadata

```bash
tidemark http://icecast.example.com:8000/stream
```

### Raw MPEGTS stream

```bash
tidemark https://example.com/stream.ts
```

### UDP multicast

```bash
tidemark udp://@239.1.1.1:5000
```

## Output

Each detected marker produces a JSON block followed by a colored summary line:

```
{
  "Type": "SCTE35",
  "Classification": "AD_START",
  "Source": "hls_manifest",
  "Tag": "#EXT-X-CUE-OUT",
  "Segment": 42,
  "Timestamp": "2026-03-20T12:00:00Z"
}
▶ [SCTE35] AD_START   Splice Insert  break=90.02s  pts=38113.135  seg=42
```

Classifications:
- **AD_START** (green) — ad break begins
- **AD_END** (yellow) — ad break ends
- **METADATA** (gray) — content metadata with no ad signal detected

## Flags

| Flag | Description |
|------|-------------|
| `--json` | Machine-readable JSON only (no summary lines). Pipe to `jq`. |
| `--quiet` | Summary lines only (no JSON blocks). |
| `--json-out FILE` | Write all markers as NDJSON to a file (alongside normal output). |
| `--timeout N` | Stop after N seconds. Default: 0 (run until Ctrl+C). |
| `--filter TYPE` | Only show markers of type: `scte35`, `id3`, or `icy`. |
| `--no-color` | Disable ANSI color codes. |

### Examples

```bash
# Quiet mode with NDJSON file output
tidemark https://example.com/live.m3u8 --quiet --json-out /tmp/markers.ndjson

# Machine-readable JSON piped to jq
tidemark https://example.com/live.m3u8 --json | jq '.Classification'

# Stop after 60 seconds, only SCTE-35 markers
tidemark https://example.com/live.m3u8 --timeout 60 --filter scte35

# No color for log files
tidemark http://icecast.example.com:8000/stream --no-color >> markers.log 2>&1
```

## Stream Type Detection

Tidemark auto-detects the stream type on startup:

| Signal | Detected As |
|--------|-------------|
| URL ends in `.m3u8` or contains `/hls/` | HLS |
| URL ends in `.ts` | MPEGTS |
| URL starts with `udp://` | UDP Multicast |
| Response header `icy-metaint` present | ICY |
| Content-Type `application/vnd.apple.mpegurl` | HLS |
| Content-Type `video/mp2t` | MPEGTS |
| Content-Type `audio/mpeg` or `audio/aac` | ICY (fallback) |

## Ad Classification

### SCTE-35

| Condition | Classification |
|-----------|---------------|
| SpliceInsert + OutOfNetworkIndicator=true | AD_START |
| SpliceInsert + OutOfNetworkIndicator=false | AD_END |
| TimeSignal + segmentation type 0x22/0x30/0x34 | AD_START |
| TimeSignal + segmentation type 0x23/0x31/0x35 | AD_END |
| SpliceNull or unrecognized | METADATA |

### ICY Metadata

| Condition | Classification |
|-----------|---------------|
| StreamTitle contains "ad", "spot", "promo", "commercial" (word boundary) | AD_START |
| StreamTitle changes to non-ad content after ad | AD_END |
| No match | METADATA |

### ID3 Tags

| Condition | Classification |
|-----------|---------------|
| Any tag value contains "ad", "spot", "promo", "commercial" (word boundary) | AD_START |
| Any tag value contains "ad_end", "content_start" | AD_END |
| No match | METADATA |

## Supported HLS SCTE-35 Tags

- `#EXT-X-SCTE35:CUE=<base64>`
- `#EXT-OATCLS-SCTE35:<base64>`
- `#EXT-X-DATERANGE` with `SCTE35-OUT` / `SCTE35-IN`
- `#EXT-X-CUE-OUT` / `#EXT-X-CUE-IN`

## Supported ID3 Frames

All structurally valid ID3v2.3 and v2.4 frames are emitted. Parsers with dedicated handling:

- `T***` — all standard text frames (TIT2 title, TPE1 artist, TALB album, TDRC date, TCON genre, TRCK track, TLEN duration, TPUB publisher, etc.)
- `TXXX` — user-defined text (description:value)
- `COMM` — comment (lang:description:text)
- `LINK` — linked information
- `WXXX` — user-defined URL (description:url)
- `W***` — standard URL frames
- `PRIV` — private data (owner:hex)
- `GEOB` — general encapsulated object
- All other valid frame IDs — best-effort text decode

## Architecture

```
URL → Detector → [ICY Reader | HLS Poller | MPEGTS Decoder | UDP Reader]
                        ↓
                  chan *Marker
                        ↓
                  Classifier (AD_START / AD_END / METADATA)
                        ↓
                  Output Printer (JSON + colored summary)
```

Each detector runs in its own goroutine, emitting `*Marker` to a shared channel. The main goroutine classifies and prints. Context cancellation (Ctrl+C / `--timeout`) propagates cleanly through the pipeline.

## Dependencies

- [cuei](https://github.com/futzu/cuei) — SCTE-35 decoding for Go (MPEGTS + Base64)

## Development

```bash
make build    # build binary
make test     # run all tests
make vet      # static analysis
make clean    # remove binary
```

## License

MIT
