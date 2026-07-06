package mpegts

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/keithah/tidemark/internal/marker"
)

func TestNewDecoder(t *testing.T) {
	d := NewDecoder()
	if d == nil {
		t.Fatal("NewDecoder returned nil")
	}
	if d.stream == nil {
		t.Fatal("stream is nil")
	}
}

func TestDecodeBufEmpty(t *testing.T) {
	d := NewDecoder()
	markers, err := d.DecodeBuf(nil)
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for nil input, got %d", len(markers))
	}
}

func TestDecodeBufEmptySlice(t *testing.T) {
	d := NewDecoder()
	markers, err := d.DecodeBuf([]byte{})
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for empty input, got %d", len(markers))
	}
}

func TestCloneDecodeDataDoesNotAliasInput(t *testing.T) {
	input := []byte{0x47, 0x00, 0x00, 0x10}
	cloned := cloneDecodeData(input)
	input[0] = 0x00

	if cloned[0] != 0x47 {
		t.Fatalf("cloned data aliased input; got first byte %#x", cloned[0])
	}
}

func TestDecodeBufReportsDecoderPanic(t *testing.T) {
	d := &Decoder{}
	markers, err := d.DecodeBuf([]byte{0x47})
	if err == nil {
		t.Fatal("expected decoder panic error")
	}
	if markers != nil {
		t.Fatalf("markers = %v, want nil on decoder panic", markers)
	}
	if d.stream == nil {
		t.Fatal("decoder stream was not reset after panic")
	}
}

func TestDecodeBufNoSCTE35(t *testing.T) {
	d := NewDecoder()
	// Valid-ish MPEGTS sync bytes but no SCTE-35
	data := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 188 bytes = 1 packet
	markers, err := d.DecodeBuf(data)
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for non-SCTE35 data, got %d", len(markers))
	}
}

func TestDecodeReaderContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		// Write some data then keep pipe open
		pw.Write(bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	d := NewDecoder()
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- d.DecodeReader(ctx, pr, ch)
	}()

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("DecodeReader error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DecodeReader did not unblock after context cancellation")
	}
}

func TestDecodeReaderEOF(t *testing.T) {
	data := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 1 MPEGTS packet
	d := NewDecoder()
	ch := make(chan *marker.Marker, 10)

	err := d.DecodeReader(context.Background(), bytes.NewReader(data), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// shortReader returns at most chunk bytes per Read, simulating a network
// reader (e.g. net/http) that does not align reads to TS packet boundaries.
type shortReader struct {
	data  []byte
	chunk int
}

func (r *shortReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

// TestDecodeReaderMisalignedChunks feeds many TS packets through a reader that
// returns chunks which are not multiples of 188 bytes. Before packet-aligned
// buffering, this drove cuei past a slice bound and panicked (issue #1).
func TestDecodeReaderMisalignedChunks(t *testing.T) {
	// 5000 packets of benign (non-SCTE35) TS data.
	pkt := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 188 bytes
	data := bytes.Repeat(pkt, 5000)

	for _, chunk := range []int{1, 7, 333, 1000, 2987} {
		d := NewDecoder()
		ch := make(chan *marker.Marker, 100)
		r := &shortReader{data: append([]byte(nil), data...), chunk: chunk}
		if err := d.DecodeReader(context.Background(), r, ch); err != nil {
			t.Fatalf("chunk=%d: DecodeReader returned error: %v", chunk, err)
		}
	}
}

func TestAlignPackets(t *testing.T) {
	pkt := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 188 bytes

	// Leading garbage before first sync byte is skipped.
	data := append([]byte{0x11, 0x22, 0x33}, pkt...)
	packets, rest := alignPackets(data)
	if len(packets) != 188 {
		t.Fatalf("packets = %d bytes, want 188", len(packets))
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %d bytes, want 0", len(rest))
	}

	// A trailing partial packet is returned as rest, not decoded.
	data = append(append([]byte(nil), pkt...), 0x47, 0x00, 0x00) // 188 + 3
	packets, rest = alignPackets(data)
	if len(packets) != 188 {
		t.Fatalf("packets = %d bytes, want 188", len(packets))
	}
	if len(rest) != 3 {
		t.Fatalf("rest = %d bytes, want 3", len(rest))
	}

	// No sync byte: nothing decoded, nothing carried.
	packets, rest = alignPackets([]byte{0x00, 0x11, 0x22})
	if packets != nil || rest != nil {
		t.Fatalf("got packets=%v rest=%v, want nil, nil", packets, rest)
	}
}

func TestDecodeBufLargeGarbage(t *testing.T) {
	d := NewDecoder()
	// Large garbage data — should not panic
	garbage := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 1000)
	markers, _ := d.DecodeBuf(garbage)
	_ = markers // no panic = pass
}

func TestDecodeSegmentExtractsTimedID3(t *testing.T) {
	d := NewDecoder()
	data := buildTimedID3TSSegmentForTest(0x0104, buildID3TextTagForTest("TIT2", "ad break"))

	result, err := d.DecodeSegment(data)
	if err != nil {
		t.Fatalf("DecodeSegment error: %v", err)
	}
	if len(result.ID3) != 1 {
		t.Fatalf("ID3 groups = %d, want 1", len(result.ID3))
	}
	if got := result.ID3[0][0].Value; got != "ad break" {
		t.Fatalf("ID3 value = %q, want ad break", got)
	}
}

func buildID3TextTagForTest(frameID, text string) []byte {
	payload := append([]byte{0x03}, []byte(text)...)
	frame := make([]byte, 10+len(payload))
	copy(frame[0:4], frameID)
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[10:], payload)

	tag := make([]byte, 10+len(frame))
	copy(tag[0:3], "ID3")
	tag[3] = 4
	encodeSynchsafeForTest(tag[6:10], len(frame))
	copy(tag[10:], frame)
	return tag
}

func encodeSynchsafeForTest(dst []byte, n int) {
	dst[0] = byte((n >> 21) & 0x7F)
	dst[1] = byte((n >> 14) & 0x7F)
	dst[2] = byte((n >> 7) & 0x7F)
	dst[3] = byte(n & 0x7F)
}

func buildTimedID3TSSegmentForTest(pid uint16, id3data []byte) []byte {
	pes := append([]byte{0x00, 0x00, 0x01, 0xbd, 0x00, 0x00, 0x80, 0x00, 0x00}, id3data...)
	var out []byte
	cc := byte(0)
	for len(pes) > 0 {
		pkt := bytes.Repeat([]byte{0xff}, tsPacketSize)
		pkt[0] = tsSyncByte
		pkt[1] = byte(pid>>8) & 0x1f
		if len(out) == 0 {
			pkt[1] |= 0x40
		}
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | (cc & 0x0f)
		cc++
		n := copy(pkt[4:], pes)
		pes = pes[n:]
		out = append(out, pkt...)
	}
	return out
}
