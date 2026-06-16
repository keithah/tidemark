package hls

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/keithah/tidemark/internal/httpclient"
	"github.com/keithah/tidemark/internal/id3"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/mpegts"
)

// MaxSegmentBytes caps buffered HLS segment data used for combined MPEG-TS and ID3 parsing.
const MaxSegmentBytes = 32 << 20

// SegmentDecoder downloads HLS media segments and returns markers found inside.
type SegmentDecoder struct {
	client *http.Client
}

func newSegmentDecoderWithClient(client *http.Client) *SegmentDecoder {
	return &SegmentDecoder{
		client: client,
	}
}

// Decode downloads and decodes one HLS media segment.
func (d *SegmentDecoder) Decode(ctx context.Context, segURL string, seg int, emit func(*marker.Marker) error) error {
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, "GET", segURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		httpclient.DrainAndClose(resp.Body)
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	defer func() { _ = resp.Body.Close() }()

	decoder := mpegts.NewDecoder()
	buf := make([]byte, 32*1024)
	segmentData := make([]byte, 0, len(buf))
	total := 0

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			total += n
			if total > MaxSegmentBytes {
				return fmt.Errorf("segment too large: exceeds %d bytes", MaxSegmentBytes)
			}
			chunk := buf[:n]
			segmentData = append(segmentData, chunk...)
			markers, decodeErr := decoder.DecodeBuf(chunk)
			if decodeErr != nil {
				return fmt.Errorf("decode mpegts: %w", decodeErr)
			}
			for _, m := range markers {
				m.Segment = seg
				m.Source = "hls_segment"
				m.Timestamp = time.Now()
				if err := emit(m); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
	}

	groups, err := id3.ParseFromMPEGTS(segmentData)
	if err != nil {
		return fmt.Errorf("parse id3: %w", err)
	}
	return emitID3Groups(groups, seg, emit)
}

func emitID3Groups(groups [][]id3.Tag, seg int, emit func(*marker.Marker) error) error {
	for _, tags := range groups {
		if len(tags) == 0 {
			continue
		}
		fields := make(map[string]string, len(tags))
		for _, tag := range tags {
			fields[tag.ID] = tag.Value
		}
		if err := emit(&marker.Marker{
			Type:      marker.MarkerID3,
			Source:    "hls_segment",
			Segment:   seg,
			Tags:      fields,
			Timestamp: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}
