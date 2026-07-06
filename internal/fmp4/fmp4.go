package fmp4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// InitInfo contains the track-level data needed to interpret CMAF fragments.
type InitInfo struct {
	Tracks map[uint32]TrackInfo
}

// TrackInfo describes one fMP4 track from the init segment.
type TrackInfo struct {
	ID        uint32
	Timescale uint32
	Handler   string
}

// FragmentTiming contains sample timing extracted from one media fragment.
type FragmentTiming struct {
	Samples []SampleTiming
	Events  []EventMessage
}

// SampleTiming is one sample's decode timeline position.
type SampleTiming struct {
	TrackID      uint32
	DecodeTime   uint64
	Duration     uint32
	StartSeconds float64
}

// EventMessage is one ISO-BMFF emsg event.
type EventMessage struct {
	SchemeIDURI             string
	Value                   string
	ID                      uint32
	Timescale               uint32
	PresentationTime        uint64
	PresentationTimeSeconds float64
	EventDuration           uint32
	MessageData             []byte
}

type boxHeader struct {
	typ     uint32
	payload []byte
}

const (
	boxEmsg = uint32('e')<<24 | uint32('m')<<16 | uint32('s')<<8 | uint32('g')
	boxHdlr = uint32('h')<<24 | uint32('d')<<16 | uint32('l')<<8 | uint32('r')
	boxMdia = uint32('m')<<24 | uint32('d')<<16 | uint32('i')<<8 | uint32('a')
	boxMdhd = uint32('m')<<24 | uint32('d')<<16 | uint32('h')<<8 | uint32('d')
	boxMoof = uint32('m')<<24 | uint32('o')<<16 | uint32('o')<<8 | uint32('f')
	boxMoov = uint32('m')<<24 | uint32('o')<<16 | uint32('o')<<8 | uint32('v')
	boxTfdt = uint32('t')<<24 | uint32('f')<<16 | uint32('d')<<8 | uint32('t')
	boxTfhd = uint32('t')<<24 | uint32('f')<<16 | uint32('h')<<8 | uint32('d')
	boxTkhd = uint32('t')<<24 | uint32('k')<<16 | uint32('h')<<8 | uint32('d')
	boxTraf = uint32('t')<<24 | uint32('r')<<16 | uint32('a')<<8 | uint32('f')
	boxTrak = uint32('t')<<24 | uint32('r')<<16 | uint32('a')<<8 | uint32('k')
	boxTrun = uint32('t')<<24 | uint32('r')<<16 | uint32('u')<<8 | uint32('n')
)

type trackBuilder struct {
	id        uint32
	timescale uint32
	handler   string
}

type trafBuilder struct {
	trackID               uint32
	baseDecodeTime        uint64
	defaultSampleDuration uint32
	sampleCount           int
	durations             []uint32
}

// ParseInit extracts track IDs, timescales, and handler types from an init segment.
func ParseInit(data []byte) (InitInfo, error) {
	info := InitInfo{Tracks: make(map[uint32]TrackInfo)}
	walkBoxes(data, func(moov boxHeader) bool {
		if moov.typ != boxMoov {
			return true
		}
		walkBoxes(moov.payload, func(trak boxHeader) bool {
			if trak.typ != boxTrak {
				return true
			}
			track, ok := parseTrack(trak.payload)
			if ok && track.id != 0 && track.timescale != 0 {
				info.Tracks[track.id] = TrackInfo{
					ID:        track.id,
					Timescale: track.timescale,
					Handler:   track.handler,
				}
			}
			return true
		})
		return true
	})
	if len(info.Tracks) == 0 {
		return info, fmt.Errorf("no fMP4 tracks found")
	}
	return info, nil
}

// ParseFragment extracts decode timing for samples in a media fragment.
func ParseFragment(init InitInfo, data []byte) (FragmentTiming, error) {
	var timing FragmentTiming
	walkBoxes(data, func(box boxHeader) bool {
		switch box.typ {
		case boxEmsg:
			if parsed, ok := parseEMSG(box.payload); ok {
				timing.Events = append(timing.Events, parsed)
			}
		case boxMoof:
			walkBoxes(box.payload, func(child boxHeader) bool {
				switch child.typ {
				case boxEmsg:
					if parsed, ok := parseEMSG(child.payload); ok {
						timing.Events = append(timing.Events, parsed)
					}
				case boxTraf:
					timing.Samples = append(timing.Samples, parseTrafSamples(init, child.payload, len(data))...)
				}
				return true
			})
		}
		return true
	})
	return timing, nil
}

// ParseFragmentReader extracts fragment timing while streaming top-level boxes.
// It buffers only boxes that can carry timing/events (moof, emsg) and discards
// large media-data boxes such as mdat.
func ParseFragmentReader(init InitInfo, r io.Reader, maxBytes int) (FragmentTiming, error) {
	var timing FragmentTiming
	if maxBytes <= 0 {
		return timing, fmt.Errorf("fragment too large: exceeds %d bytes", maxBytes)
	}

	lr := &io.LimitedReader{R: r, N: int64(maxBytes) + 1}
	for {
		box, ok, err := readStreamBox(lr)
		if err != nil {
			return FragmentTiming{}, err
		}
		if !ok {
			break
		}
		if lr.N == 0 {
			return FragmentTiming{}, fmt.Errorf("fragment too large: exceeds %d bytes", maxBytes)
		}
		switch box.typ {
		case boxEmsg:
			payload, err := readStreamBoxPayload(lr, box.payloadSize)
			if err != nil {
				return FragmentTiming{}, err
			}
			if parsed, ok := parseEMSG(payload); ok {
				timing.Events = append(timing.Events, parsed)
			}
		case boxMoof:
			payload, err := readStreamBoxPayload(lr, box.payloadSize)
			if err != nil {
				return FragmentTiming{}, err
			}
			walkBoxes(payload, func(child boxHeader) bool {
				switch child.typ {
				case boxEmsg:
					if parsed, ok := parseEMSG(child.payload); ok {
						timing.Events = append(timing.Events, parsed)
					}
				case boxTraf:
					timing.Samples = append(timing.Samples, parseTrafSamples(init, child.payload, maxBytes)...)
				}
				return true
			})
		default:
			if err := discardStreamBoxPayload(lr, box.payloadSize); err != nil {
				return FragmentTiming{}, err
			}
		}
		if lr.N == 0 {
			return FragmentTiming{}, fmt.Errorf("fragment too large: exceeds %d bytes", maxBytes)
		}
	}
	return timing, nil
}

type streamBoxHeader struct {
	typ         uint32
	payloadSize uint64
}

func readStreamBox(r *io.LimitedReader) (streamBoxHeader, bool, error) {
	header := make([]byte, 8)
	n, err := io.ReadFull(r, header)
	if err != nil {
		if err == io.EOF && n == 0 {
			return streamBoxHeader{}, false, nil
		}
		return streamBoxHeader{}, false, err
	}
	size := uint64(binary.BigEndian.Uint32(header[:4]))
	headerSize := uint64(8)
	if size == 1 {
		extended := make([]byte, 8)
		if _, err := io.ReadFull(r, extended); err != nil {
			return streamBoxHeader{}, false, err
		}
		size = binary.BigEndian.Uint64(extended)
		headerSize = 16
	} else if size == 0 {
		size = uint64(r.N) + headerSize
	}
	if size < headerSize {
		return streamBoxHeader{}, false, fmt.Errorf("invalid fMP4 box size %d", size)
	}
	return streamBoxHeader{
		typ:         binary.BigEndian.Uint32(header[4:8]),
		payloadSize: size - headerSize,
	}, true, nil
}

func readStreamBoxPayload(r *io.LimitedReader, size uint64) ([]byte, error) {
	if size > uint64(r.N) || size > uint64(int(^uint(0)>>1)) {
		return nil, io.ErrUnexpectedEOF
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func discardStreamBoxPayload(r *io.LimitedReader, size uint64) error {
	if size > uint64(r.N) {
		return io.ErrUnexpectedEOF
	}
	_, err := io.CopyN(io.Discard, r, int64(size))
	return err
}

func parseTrafSamples(init InitInfo, data []byte, fragmentSize int) []SampleTiming {
	frag := parseTraf(data)
	track := init.Tracks[frag.trackID]
	if frag.trackID == 0 || track.Timescale == 0 {
		return nil
	}
	// Synthesize per-sample durations from the track default when the trun
	// carried none. sampleCount is an untrusted 32-bit field; a fragment can
	// declare at most one sample per byte of media data, so reject counts larger
	// than the fragment itself rather than allocating from the raw value.
	if len(frag.durations) == 0 && frag.defaultSampleDuration > 0 &&
		frag.sampleCount > 0 && frag.sampleCount <= fragmentSize {
		frag.durations = make([]uint32, frag.sampleCount)
		for i := range frag.durations {
			frag.durations[i] = frag.defaultSampleDuration
		}
	}

	samples := make([]SampleTiming, 0, len(frag.durations))
	decodeTime := frag.baseDecodeTime
	for _, duration := range frag.durations {
		samples = append(samples, SampleTiming{
			TrackID:      frag.trackID,
			DecodeTime:   decodeTime,
			Duration:     duration,
			StartSeconds: float64(decodeTime) / float64(track.Timescale),
		})
		decodeTime += uint64(duration)
	}
	return samples
}

func parseEMSG(data []byte) (EventMessage, bool) {
	if len(data) < 4 {
		return EventMessage{}, false
	}
	version := data[0]
	if version == 1 {
		return parseEMSGVersion1(data)
	}
	return parseEMSGVersion0(data)
}

func parseEMSGVersion1(data []byte) (EventMessage, bool) {
	if len(data) < 24 {
		return EventMessage{}, false
	}
	timescale := binary.BigEndian.Uint32(data[4:8])
	if timescale == 0 {
		return EventMessage{}, false
	}
	event := EventMessage{
		Timescale:        timescale,
		PresentationTime: binary.BigEndian.Uint64(data[8:16]),
		EventDuration:    binary.BigEndian.Uint32(data[16:20]),
		ID:               binary.BigEndian.Uint32(data[20:24]),
	}
	rest := data[24:]
	scheme, rest, ok := readCString(rest)
	if !ok {
		return EventMessage{}, false
	}
	value, rest, ok := readCString(rest)
	if !ok {
		return EventMessage{}, false
	}
	event.SchemeIDURI = scheme
	event.Value = value
	event.PresentationTimeSeconds = float64(event.PresentationTime) / float64(event.Timescale)
	event.MessageData = append([]byte(nil), rest...)
	return event, true
}

func parseEMSGVersion0(data []byte) (EventMessage, bool) {
	rest := data[4:]
	scheme, rest, ok := readCString(rest)
	if !ok || len(rest) < 16 {
		return EventMessage{}, false
	}
	value, rest, ok := readCString(rest)
	if !ok || len(rest) < 16 {
		return EventMessage{}, false
	}
	timescale := binary.BigEndian.Uint32(rest[:4])
	if timescale == 0 {
		return EventMessage{}, false
	}
	presentationTime := uint64(binary.BigEndian.Uint32(rest[4:8]))
	event := EventMessage{
		SchemeIDURI:             scheme,
		Value:                   value,
		Timescale:               timescale,
		PresentationTime:        presentationTime,
		PresentationTimeSeconds: float64(presentationTime) / float64(timescale),
		EventDuration:           binary.BigEndian.Uint32(rest[8:12]),
		ID:                      binary.BigEndian.Uint32(rest[12:16]),
		MessageData:             append([]byte(nil), rest[16:]...),
	}
	return event, true
}

func readCString(data []byte) (string, []byte, bool) {
	idx := bytes.IndexByte(data, 0)
	if idx < 0 {
		return "", nil, false
	}
	return string(data[:idx]), data[idx+1:], true
}

func parseTrack(data []byte) (trackBuilder, bool) {
	var track trackBuilder
	walkBoxes(data, func(child boxHeader) bool {
		switch child.typ {
		case boxTkhd:
			track.id = parseVersionedUint32(child.payload)
		case boxMdia:
			walkBoxes(child.payload, func(mdiaChild boxHeader) bool {
				switch mdiaChild.typ {
				case boxMdhd:
					track.timescale = parseVersionedUint32(mdiaChild.payload)
				case boxHdlr:
					track.handler = parseHDLRHandler(mdiaChild.payload)
				}
				return true
			})
		}
		return true
	})
	return track, track.id != 0 || track.timescale != 0 || track.handler != ""
}

func parseTraf(data []byte) trafBuilder {
	var frag trafBuilder
	walkBoxes(data, func(child boxHeader) bool {
		switch child.typ {
		case boxTfhd:
			frag.trackID, frag.defaultSampleDuration = parseTFHD(child.payload)
		case boxTfdt:
			frag.baseDecodeTime = parseTFDTBaseDecodeTime(child.payload)
		case boxTrun:
			frag.sampleCount, frag.durations = parseTRUN(child.payload)
		}
		return true
	})
	return frag
}

// parseVersionedUint32 reads a big-endian uint32 that both tkhd (track ID) and
// mdhd (timescale) place at offset 12 in their version-0 layout and offset 20
// in version 1. The two boxes share this full-box header layout exactly.
func parseVersionedUint32(data []byte) uint32 {
	if len(data) < 16 {
		return 0
	}
	if version := data[0]; version == 1 {
		if len(data) < 28 {
			return 0
		}
		return binary.BigEndian.Uint32(data[20:24])
	}
	return binary.BigEndian.Uint32(data[12:16])
}

func parseHDLRHandler(data []byte) string {
	if len(data) < 12 {
		return ""
	}
	return string(data[8:12])
}

func parseTFHD(data []byte) (trackID, defaultSampleDuration uint32) {
	if len(data) < 8 {
		return 0, 0
	}
	flags := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	trackID = binary.BigEndian.Uint32(data[4:8])
	offset := 8
	if flags&0x000001 != 0 {
		offset += 8
	}
	if flags&0x000002 != 0 {
		offset += 4
	}
	if flags&0x000008 != 0 && offset+4 <= len(data) {
		defaultSampleDuration = binary.BigEndian.Uint32(data[offset : offset+4])
	}
	return trackID, defaultSampleDuration
}

func parseTFDTBaseDecodeTime(data []byte) uint64 {
	if len(data) < 8 {
		return 0
	}
	version := data[0]
	if version == 1 {
		if len(data) < 12 {
			return 0
		}
		return binary.BigEndian.Uint64(data[4:12])
	}
	return uint64(binary.BigEndian.Uint32(data[4:8]))
}

func parseTRUN(data []byte) (int, []uint32) {
	if len(data) < 8 {
		return 0, nil
	}
	flags := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	sampleCount := int(binary.BigEndian.Uint32(data[4:8]))
	offset := 8
	if flags&0x000001 != 0 {
		offset += 4
	}
	if flags&0x000004 != 0 {
		offset += 4
	}
	if flags&0x000100 == 0 {
		return sampleCount, nil
	}
	// Cap the preallocated capacity by the bytes actually available: each entry
	// consumes at least 4 bytes, so sampleCount can never exceed len(data)/4 here.
	// This prevents a bogus sampleCount from forcing a huge allocation before the
	// bounded read loop runs.
	capHint := sampleCount
	if maxEntries := (len(data) - offset) / 4; capHint > maxEntries {
		capHint = maxEntries
	}
	if capHint < 0 {
		capHint = 0
	}
	durations := make([]uint32, 0, capHint)
	for i := 0; i < sampleCount && offset+4 <= len(data); i++ {
		durations = append(durations, binary.BigEndian.Uint32(data[offset:offset+4]))
		offset += 4
		if flags&0x000200 != 0 {
			offset += 4
		}
		if flags&0x000400 != 0 {
			offset += 4
		}
		if flags&0x000800 != 0 {
			offset += 4
		}
	}
	return sampleCount, durations
}

func walkBoxes(data []byte, visit func(boxHeader) bool) {
	for len(data) >= 8 {
		size := uint64(binary.BigEndian.Uint32(data[:4]))
		headerSize := uint64(8)
		if size == 1 {
			if len(data) < 16 {
				break
			}
			size = binary.BigEndian.Uint64(data[8:16])
			headerSize = 16
		} else if size == 0 {
			// size==0 means "box extends to the end of the enclosure" (ISO
			// 14496-12 §4.2), legal for the last top-level box.
			size = uint64(len(data))
		}
		if size < headerSize || size > uint64(len(data)) {
			break
		}
		box := boxHeader{
			typ:     binary.BigEndian.Uint32(data[4:8]),
			payload: data[headerSize:size],
		}
		if !visit(box) {
			return
		}
		data = data[size:]
	}
}
