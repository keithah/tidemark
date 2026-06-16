package icy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/keithah/tidemark/internal/httpclient"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/pipeline"
)

const (
	defaultMetaInt = 16000

	// MaxMetaInt caps the remote icy-metaint value before allocating an audio buffer.
	MaxMetaInt = 1 << 20

	audioDiscardBufferSize = 32 * 1024
	maxMetadataSize        = 255 * 16
)

// Reader reads ICY metadata from an Icecast/SHOUTcast stream.
type Reader struct {
	url     string
	metaInt int
}

// NewReader creates a new ICY reader for the given URL and metaint value.
// If metaInt is 0, the default (16000) is used.
func NewReader(url string, metaInt int) *Reader {
	if metaInt == 0 {
		metaInt = defaultMetaInt
	}
	return &Reader{url: url, metaInt: metaInt}
}

// Read connects to the ICY stream and emits Markers on the channel when
// StreamTitle changes. It blocks until the context is cancelled or the
// stream ends.
func (r *Reader) Read(ctx context.Context, ch chan<- *marker.Marker) error {
	client := httpclient.NewStreaming()
	req, err := http.NewRequestWithContext(ctx, "GET", r.url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Icy-MetaData", "1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		httpclient.DrainAndClose(resp.Body)
		return fmt.Errorf("connect: status %d", resp.StatusCode)
	}
	resp.Body = httpclient.WithIdleReadTimeout(resp.Body, httpclient.DefaultIdleReadTimeout)
	defer func() { _ = resp.Body.Close() }()

	return r.readStream(ctx, resp.Body, ch)
}

func (r *Reader) readStream(ctx context.Context, stream io.Reader, ch chan<- *marker.Marker) error {
	if r.metaInt <= 0 || r.metaInt > MaxMetaInt {
		return fmt.Errorf("invalid icy-metaint: %d", r.metaInt)
	}

	reader := bufio.NewReader(stream)
	audioBuf := make([]byte, min(r.metaInt, audioDiscardBufferSize))
	metaBuf := make([]byte, maxMetadataSize)
	var prevTitle string

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read audio data
		n, err := discardAudio(reader, audioBuf, r.metaInt)
		if err != nil {
			if n == 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
				return nil // clean end
			}
			return fmt.Errorf("read audio: %w", err)
		}

		// Read size byte
		sb, err := reader.ReadByte()
		if err != nil {
			return fmt.Errorf("read size byte: %w", err)
		}

		metaSize := int(sb) * 16
		if metaSize == 0 {
			continue
		}

		// Read metadata
		meta := metaBuf[:metaSize]
		if _, err = io.ReadFull(reader, meta); err != nil {
			return fmt.Errorf("read metadata: %w", err)
		}

		// Sanitize and parse
		sanitized := sanitize(meta)
		if sanitized == "" {
			continue
		}

		fields := parseFields(sanitized)
		title := fields["StreamTitle"]
		if title == "" || title == prevTitle {
			continue
		}
		prevTitle = title

		m := &marker.Marker{
			Type:      marker.MarkerICY,
			Source:    "icy_stream",
			Fields:    fields,
			Timestamp: time.Now(),
		}
		if err := pipeline.SendMarker(ctx, ch, m); err != nil {
			return err
		}
	}
}

func discardAudio(reader io.Reader, buf []byte, bytesToDiscard int) (int, error) {
	total := 0
	for total < bytesToDiscard {
		n := bytesToDiscard - total
		if n > len(buf) {
			n = len(buf)
		}
		read, err := io.ReadFull(reader, buf[:n])
		total += read
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// sanitize removes null bytes and non-printable characters, ensuring valid UTF-8.
func sanitize(data []byte) string {
	// Trim null bytes
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return ""
	}

	if !utf8.Valid(data) {
		return "[binary data]"
	}

	var b strings.Builder
	b.Grow(len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		data = data[size:]
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseFields parses ICY metadata into a map of key=value pairs.
func parseFields(meta string) map[string]string {
	fields := make(map[string]string, strings.Count(meta, ";")+1)
	for {
		part, rest, found := strings.Cut(meta, ";")
		part = strings.TrimSpace(part)
		if part != "" {
			key, val, ok := strings.Cut(part, "=")
			if ok {
				if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
					val = val[1 : len(val)-1]
				}
				fields[key] = val
			}
		}
		if !found {
			break
		}
		meta = rest
	}
	return fields
}
