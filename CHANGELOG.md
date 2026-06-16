# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.1] - 2026-06-16

### Fixed

- Improved HLS polling reliability with conditional manifest requests, bounded segment tracking, ordered segment marker emission, faster polling tests, and fail-fast handling for permanent manifest errors.
- Added streaming idle-read timeouts and stronger cancellation cleanup for ICY, MPEGTS, UDP, and HLS segment readers.
- Report MPEGTS decoder failures instead of silently treating recovered decoder panics as empty marker results.
- Hardened ID3 scanning/parsing for incremental streams, oversized tags, lower allocation parsing, and safe terminal/table output.
- Reduced hot-path allocations in marker classification, JSON output, enum marshaling, HLS attribute parsing, and ID3 segment marker emission.
- Reworked output flushing and JSON file closing so write, flush, sync, and close errors are surfaced correctly.
- Restored cached default test paths in `make test`, release CI, and the release workflow while keeping `make test-fresh` for explicit uncached runs.

## [0.3.0] - 2026-06-04

### Added

- **Extended ID3v2 frame support** — LINK, WXXX, W* (URL), COMM, and all other valid frames are now emitted; previously any frame ID not in a hardcoded list was silently dropped
- **Generic frame fallback** — unknown but structurally valid ID3 frames are parsed and emitted using a best-effort text decoder, so no metadata is lost

### Fixed

- **Per-event ID3 markers** — a segment containing multiple timed ID3 packets (multiple PES streams) now emits one marker per event; previously all frames were collapsed into a single map, losing duplicate frame IDs and making classification order-dependent
- **METADATA classification label** — markers that carry content metadata but no ad signal now show `METADATA` instead of `UNKNOWN`

[0.3.1]: https://github.com/keithah/tidemark-go/releases/tag/v0.3.1
[0.3.0]: https://github.com/keithah/tidemark-go/releases/tag/v0.3.0

## [0.2.1] - 2026-04-29

### Fixed

- Release artifacts now named `tidemark_<os>_<arch>` instead of `tidemark-go_<os>_<arch>`

[0.2.1]: https://github.com/keithah/tidemark-go/releases/tag/v0.2.1

## [0.2.0] - 2026-04-29

### Added

- **Full ID3v2 text frame coverage** — all standard `T***` frames (TPE1 artist, TALB album, TCON genre, TDRC date, TRCK track, TPOS disc, etc.) are now parsed; previously only TIT2 and TIT3 were extracted
- **MPEGTS timed_id3 extraction** — new `ParseFromMPEGTS` function correctly reassembles ID3 payloads from MPEGTS TS packet PES streams, enabling reliable metadata extraction from MPEGTS-segmented HLS streams (stream type 0x15 / timed_id3); previously tags spanning more than one TS packet payload (~180 bytes) were silently corrupted

[0.2.0]: https://github.com/keithah/tidemark-go/releases/tag/v0.2.0

## [0.1.0] - 2026-03-20

### Added

- **Stream auto-detection** — automatically identifies HLS, MPEGTS, ICY, and UDP streams from URL patterns and HTTP headers
- **ICY metadata reader** — connects to Icecast/SHOUTcast streams, parses the binary ICY protocol, extracts StreamTitle and other fields
- **HLS manifest poller** — polls live and VOD manifests with EXT-X-MEDIA-SEQUENCE tracking, master-to-media playlist resolution, and segment deduplication
- **SCTE-35 decoding** — parses five HLS tag families (EXT-X-SCTE35, EXT-OATCLS-SCTE35, EXT-X-DATERANGE, EXT-X-CUE-OUT/IN) and decodes base64/hex payloads via cuei
- **MPEGTS segment decoding** — extracts SCTE-35 from transport stream packets in HLS segments and direct .ts URL streams
- **UDP multicast support** — reads 1316-byte MPEGTS datagrams from multicast groups
- **ID3v2 tag extraction** — hand-rolled parser for TIT2, TIT3, TXXX, PRIV, GEOB frames with v2.3/v2.4 and UTF-8/UTF-16/ISO-8859-1 support
- **Ad classification engine** — classifies markers as AD_START, AD_END, or UNKNOWN using protocol-specific rules (SCTE-35 command/descriptor types, ICY keyword matching, ID3 frame content)
- **Structured output** — indented JSON blocks + ANSI-colored summary lines, with three output modes (default, `--json`, `--quiet`)
- **NDJSON file output** — `--json-out FILE` writes newline-delimited JSON alongside normal output
- **Marker filtering** — `--filter TYPE` shows only scte35, id3, or icy markers
- **Timeout support** — `--timeout N` stops after N seconds
- **Graceful shutdown** — Ctrl+C / SIGTERM exits cleanly with marker count
- **Startup banner** — shows URL, detected stream type, filter, and output mode on stderr
- **167 tests** across 10 packages with zero failures

[0.1.0]: https://github.com/keithah/tidemark-go/releases/tag/v0.1.0
