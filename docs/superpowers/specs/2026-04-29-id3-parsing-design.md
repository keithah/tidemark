# ID3 Parsing — Design Spec

**Date:** 2026-04-29  
**Scope:** `internal/id3`, `internal/hls/poller.go`

> Status: Implemented. This is a historical design note; current maintainer behavior is documented in `docs/reference.md`. `ParseFromMPEGTS` now returns grouped timed-ID3 events as `[][]Tag`, and HLS segment decoding emits one marker per event.

---

## Problem

Two gaps prevent full ID3 extraction from HLS streams:

1. **Missing T-frames.** `parseFrame` only handles TIT2 and TIT3 explicitly; TPE1 (artist), TALB (album), TCON, TDRC, and all other standard text frames return `nil`.

2. **MPEGTS framing not handled.** `id3.Parse(data)` scans raw bytes for the `ID3` magic header. For AAC segments with prepended ID3 this works. For MPEGTS segments carrying `timed_id3` (stream type `0x15`), the ID3 payload is wrapped in PES packets inside 188-byte TS packets. TS packet headers (4 bytes every 188 bytes) interleave with the ID3 frame data, corrupting any tag that spans more than one TS packet payload (~180 bytes).

The test stream (`lotus-music.stingray.com` HLS) has a confirmed `timed_id3` data stream (Stream #0:1 per ffplay), which exercises both gaps.

---

## Design

### 1. Generic T-frame parsing (`internal/id3/id3.go`)

Change one `case` in `parseFrame`:

```go
// Before
case id == "TIT2" || id == "TIT3":
    return parseTextFrame(id, data)

// After
case strings.HasPrefix(id, "T") && id != "TXXX":
    return parseTextFrame(id, data)
```

All standard text frames (TPE1, TALB, TCON, TDRC, TYER, TPOS, TRCK, etc.) now route to the existing `decodeText` logic, which already handles ISO-8859-1, UTF-16 with BOM, UTF-16BE, and UTF-8.

TXXX, PRIV, and GEOB keep their existing dedicated parsers.

### 2. MPEGTS PES extraction (`internal/id3/id3.go`)

New exported function:

```go
func ParseFromMPEGTS(data []byte) ([][]Tag, error)
```

**Algorithm:**

1. **Detect MPEGTS.** If `len(data) >= 188`, `data[0] == 0x47`, and `len(data) % 188 == 0`: treat as MPEGTS. Otherwise fall back to `Parse(data)` — raw AAC segments and any non-TS input continue to work unchanged.

2. **Walk 188-byte TS packets.** For each packet:
   - Parse the 4-byte TS header: extract PID (13 bits), PUSI flag (payload_unit_start_indicator), and adaptation_field_control (2 bits).
   - If adaptation field present (`afc & 0x02 != 0`): read the length byte and skip that many bytes.
   - Collect remaining bytes into `buf map[uint16][]byte` keyed by PID.
   - When PUSI=1: if the existing buffer for that PID starts with `ID3`, flush it to the completed list; reset the buffer to the current payload. If the buffer is empty or doesn't start with `ID3`, reset to current payload (not ID3 data, discard).

3. **Flush remaining buffers** after all packets: any PID buffer starting with `ID3` is added to the completed list.

4. **Strip PES header.** Each completed blob may be preceded by a PES header. Search for the first occurrence of bytes `0x49 0x44 0x33` (`ID3`) within the first 32 bytes of the blob. If found, slice from that offset before passing to `Parse`. If not found within 32 bytes, skip the blob — it is not a valid ID3 PES payload.

5. **Group results.** Call `Parse` on each extracted blob and return one `[]Tag` group per timed-ID3 event.

### 3. HLS poller wire-up (`internal/hls/poller.go`)

One line change in `downloadAndDecode`:

```go
// Before
tags, _ := id3.Parse(data)

// After
tagGroups, _ := id3.ParseFromMPEGTS(data)
```

The segment decoder emits one ID3 marker for each returned tag group so repeated frame IDs in separate timed-ID3 events are not flattened together.

---

## Data Flow

```
HLS segment bytes
       │
       ▼
id3.ParseFromMPEGTS(data)
       │
       ├─ MPEGTS? ──yes──► walk TS packets → reassemble PES per PID
       │                         │
       │                         ▼
       │                   strip PES header → id3.Parse(blob)
       │
       └─ no ──────────────────► id3.Parse(data)
                                      │
                                      ▼
                               [][]Tag{
                                 {
                                   {ID: "TIT2", Value: "Song Title"},
                                   {ID: "TPE1", Value: "Artist Name"},
                                   {ID: "TALB", Value: "Album Name"},
                                   {ID: "TXXX", Value: "ad_id:abc123"},
                                 },
                               }
                                      │
                                      ▼
                            marker.Marker{
                              Type:   MarkerID3,
                              Source: "hls_segment",
                              Tags:   map[string]string{...},
                            }
```

---

## Error Handling

- Malformed TS packets (wrong sync byte, truncated): skip the offending packet and continue. Return whatever tags were successfully parsed.
- PES payload that starts with `ID3` but is corrupt: `id3.Parse` already returns gracefully with whatever frames it could read.
- Non-MPEGTS input to `ParseFromMPEGTS`: falls back to `Parse`, wraps the result in one group, and returns no error.

---

## Testing

All tests in `internal/id3/id3_test.go`. New helper: `buildMPEGTSSegment(pid uint16, pesHeader []byte, id3data []byte) []byte` — wraps id3data into 188-byte TS packets with a minimal PES header, first packet PUSI=1, subsequent PUSI=0.

| Test | What it verifies |
|------|-----------------|
| `TestGenericTextFrameTPE1` | TPE1 parses as `{ID:"TPE1", Value:"Some Artist"}` |
| `TestGenericTextFrameTALB` | TALB parses correctly |
| `TestTXXXStillUsesOwnParser` | TXXX is not caught by generic T-frame path |
| `TestParseFromMPEGTSFallback` | Non-TS bytes fall through to `Parse` |
| `TestParseFromMPEGTSSinglePacket` | ID3 tag in one TS packet: TPE1/TIT2 extracted |
| `TestParseFromMPEGTSMultiPacket` | ID3 tag spanning two TS packets: full tag reassembled |
| `TestParseFromMPEGTSMultiplePIDs` | Two PIDs, one non-ID3: only ID3 PID extracted |
| `TestParseFromMPEGTSMultipleID3Events` | Multiple timed-ID3 events remain separate groups |
| `TestParseFromMPEGTSNonID3PES` | PES payloads without `ID3` magic ignored cleanly |

---

## Files Changed

| File | Change |
|------|--------|
| `internal/id3/id3.go` | Generic T-frame case; add `ParseFromMPEGTS` |
| `internal/id3/id3_test.go` | 8 new tests + `buildMPEGTSSegment` helper |
| `internal/hls/poller.go` | `id3.Parse` → `id3.ParseFromMPEGTS` (1 line) |

No changes to marker struct, classifier, output, or any other package.
