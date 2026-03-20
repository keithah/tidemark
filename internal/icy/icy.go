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

	"github.com/keithah/tidemark/internal/marker"
)

const defaultMetaInt = 16000

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
	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", r.url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Icy-MetaData", "1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return r.readStream(ctx, resp.Body, ch)
}

// ReadFrom reads ICY metadata from an arbitrary reader (for testing).
func (r *Reader) ReadFrom(ctx context.Context, stream io.Reader, ch chan<- *marker.Marker) error {
	return r.readStream(ctx, stream, ch)
}

func (r *Reader) readStream(ctx context.Context, stream io.Reader, ch chan<- *marker.Marker) error {
	reader := bufio.NewReader(stream)
	buf := make([]byte, r.metaInt)
	var prevTitle string

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read audio data
		n, err := io.ReadFull(reader, buf)
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
		meta := make([]byte, metaSize)
		_, err = io.ReadFull(reader, meta)
		if err != nil {
			return fmt.Errorf("read metadata: %w", err)
		}

		// Sanitize and parse
		sanitized := sanitize(meta)
		if sanitized == "" {
			continue
		}

		title := parseStreamTitle(sanitized)
		if title == "" || title == prevTitle {
			continue
		}
		prevTitle = title

		fields := parseFields(sanitized)
		m := &marker.Marker{
			Type:      marker.MarkerICY,
			Source:    "icy_stream",
			Fields:   fields,
			Timestamp: time.Now(),
		}
		ch <- m
	}
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
	for _, r := range string(data) {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseStreamTitle extracts StreamTitle from ICY metadata string.
func parseStreamTitle(meta string) string {
	const prefix = "StreamTitle='"
	for _, part := range strings.Split(meta, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) && len(part) > len(prefix) {
			// Remove trailing single quote
			val := part[len(prefix):]
			if strings.HasSuffix(val, "'") {
				val = val[:len(val)-1]
			}
			return val
		}
	}
	return ""
}

// parseFields parses ICY metadata into a map of key=value pairs.
func parseFields(meta string) map[string]string {
	fields := make(map[string]string)
	for _, part := range strings.Split(meta, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Handle key='value' format
		eqIdx := strings.Index(part, "=")
		if eqIdx < 0 {
			continue
		}
		key := part[:eqIdx]
		val := part[eqIdx+1:]
		// Strip surrounding quotes
		if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
			val = val[1 : len(val)-1]
		}
		fields[key] = val
	}
	return fields
}
