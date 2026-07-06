package hls

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/keithah/tidemark/internal/fmp4"
	"github.com/keithah/tidemark/internal/httpclient"
	"github.com/keithah/tidemark/internal/id3"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/mpegts"
)

// MaxSegmentBytes caps buffered HLS segment data used for combined MPEG-TS and ID3 parsing.
const MaxSegmentBytes = 32 << 20

// MaxInitSegmentBytes caps fMP4 init-map downloads. Init segments should only
// contain metadata boxes, not media payloads.
const MaxInitSegmentBytes = 2 << 20

// maxInitCacheEntries bounds the fMP4 init-segment cache so a long-running
// stream that rotates its EXT-X-MAP URI (SSAI, discontinuities) cannot grow
// memory without limit.
const maxInitCacheEntries = 256

// SegmentDecoder downloads HLS media segments and returns markers found inside.
type SegmentDecoder struct {
	client       *http.Client
	initMu       sync.Mutex
	initCache    map[string]fmp4.InitInfo
	initOrder    []string
	initNext     int
	initFetching map[string]*initFetch
}

type initFetch struct {
	done chan struct{}
	info fmp4.InitInfo
	err  error
}

func newSegmentDecoderWithClient(client *http.Client) *SegmentDecoder {
	return &SegmentDecoder{
		client:       client,
		initCache:    make(map[string]fmp4.InitInfo, maxInitCacheEntries),
		initOrder:    make([]string, 0, maxInitCacheEntries),
		initFetching: make(map[string]*initFetch),
	}
}

// rememberInit stores an init map under a FIFO cap. Caller holds initMu.
func (d *SegmentDecoder) rememberInit(mapURL string, info fmp4.InitInfo) {
	if _, ok := d.initCache[mapURL]; ok {
		d.initCache[mapURL] = info
		return
	}
	if len(d.initOrder) < maxInitCacheEntries {
		d.initOrder = append(d.initOrder, mapURL)
	} else {
		delete(d.initCache, d.initOrder[d.initNext])
		d.initOrder[d.initNext] = mapURL
		d.initNext = (d.initNext + 1) % maxInitCacheEntries
	}
	d.initCache[mapURL] = info
}

// Decode downloads and decodes one HLS media segment.
func (d *SegmentDecoder) Decode(ctx context.Context, segURL, mapURL string, seg int, emit func(*marker.Marker) error) error {
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if mapURL != "" {
		resp, err := d.fetchResponse(dlCtx, segURL)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		return d.decodeFMP4(ctx, mapURL, resp.Body, seg, emit)
	}

	result, decodeErr := d.decodeMPEGTS(dlCtx, segURL)
	if decodeErr != nil {
		return fmt.Errorf("decode mpegts: %w", decodeErr)
	}
	return emitSegmentResult(result, seg, emit)
}

func (d *SegmentDecoder) decodeMPEGTS(ctx context.Context, segURL string) (mpegts.SegmentResult, error) {
	resp, err := d.fetchResponse(ctx, segURL)
	if err != nil {
		return mpegts.SegmentResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	limited := &io.LimitedReader{R: resp.Body, N: MaxSegmentBytes + 1}
	prefix := make([]byte, 188)
	n, readErr := io.ReadFull(limited, prefix)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return mpegts.SegmentResult{}, fmt.Errorf("read body: %w", readErr)
	}
	if n < 188 || prefix[0] != 0x47 {
		data := append([]byte(nil), prefix[:n]...)
		rest, err := io.ReadAll(limited)
		if err != nil {
			return mpegts.SegmentResult{}, fmt.Errorf("read body: %w", err)
		}
		data = append(data, rest...)
		if limited.N == 0 {
			return mpegts.SegmentResult{}, fmt.Errorf("segment too large: exceeds %d bytes", MaxSegmentBytes)
		}
		return mpegts.NewDecoder().DecodeSegment(data)
	}

	decoder := mpegts.NewDecoder()
	result, err := decoder.DecodeSegmentReader(ctx, io.MultiReader(bytes.NewReader(prefix[:n]), limited))
	if err != nil {
		return mpegts.SegmentResult{}, err
	}
	if limited.N == 0 {
		return mpegts.SegmentResult{}, fmt.Errorf("segment too large: exceeds %d bytes", MaxSegmentBytes)
	}
	return result, nil
}

func emitSegmentResult(result mpegts.SegmentResult, seg int, emit func(*marker.Marker) error) error {
	for _, m := range result.SCTE35 {
		m.Segment = seg
		m.Source = "hls_segment"
		m.Timestamp = time.Now()
		if err := emit(m); err != nil {
			return err
		}
	}

	return emitID3Groups(result.ID3, seg, emit)
}

func (d *SegmentDecoder) fetchResponse(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		httpclient.DrainAndClose(resp.Body)
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp, nil
}

func (d *SegmentDecoder) fetchBytes(ctx context.Context, url string, maxBytes int, label string) ([]byte, error) {
	resp, err := d.fetchResponse(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("%s too large: exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

func (d *SegmentDecoder) decodeFMP4(ctx context.Context, mapURL string, segment io.Reader, seg int, emit func(*marker.Marker) error) error {
	init, err := d.initInfo(ctx, mapURL)
	if err != nil {
		return err
	}
	timing, err := fmp4.ParseFragmentReader(init, segment, MaxSegmentBytes)
	if err != nil {
		return fmt.Errorf("parse fmp4 fragment: %w", err)
	}
	if err := emitFMP4Timeline(init, timing.Samples, seg, emit); err != nil {
		return err
	}
	for _, event := range timing.Events {
		if err := emit(&marker.Marker{
			Type:           marker.MarkerFMP4,
			Classification: marker.Unknown,
			Source:         "hls_fmp4",
			Tag:            "emsg",
			PTS:            event.PresentationTimeSeconds,
			Segment:        seg,
			Fields: map[string]string{
				"scheme_id_uri":     event.SchemeIDURI,
				"value":             event.Value,
				"event_id":          strconv.FormatUint(uint64(event.ID), 10),
				"timescale":         strconv.FormatUint(uint64(event.Timescale), 10),
				"presentation_time": strconv.FormatUint(event.PresentationTime, 10),
				"event_duration":    strconv.FormatUint(uint64(event.EventDuration), 10),
				"message_data":      string(event.MessageData),
			},
			Timestamp: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

type fmp4Timeline struct {
	trackID         uint32
	firstDecodeTime uint64
	endDecodeTime   uint64
	firstPTS        float64
	duration        uint64
	sampleCount     int
}

func emitFMP4Timeline(init fmp4.InitInfo, samples []fmp4.SampleTiming, seg int, emit func(*marker.Marker) error) error {
	timelines := make(map[uint32]*fmp4Timeline)
	order := make([]uint32, 0, 1)
	for _, sample := range samples {
		timeline, ok := timelines[sample.TrackID]
		if !ok {
			timeline = &fmp4Timeline{
				trackID:         sample.TrackID,
				firstDecodeTime: sample.DecodeTime,
				firstPTS:        sample.StartSeconds,
			}
			timelines[sample.TrackID] = timeline
			order = append(order, sample.TrackID)
		}
		timeline.sampleCount++
		timeline.duration += uint64(sample.Duration)
		timeline.endDecodeTime = sample.DecodeTime + uint64(sample.Duration)
	}

	for _, trackID := range order {
		timeline := timelines[trackID]
		track := init.Tracks[trackID]
		durationSeconds := 0.0
		if track.Timescale > 0 {
			durationSeconds = float64(timeline.duration) / float64(track.Timescale)
		}
		if err := emit(&marker.Marker{
			Type:           marker.MarkerFMP4,
			Classification: marker.Unknown,
			Source:         "hls_fmp4",
			Tag:            "timeline",
			PTS:            timeline.firstPTS,
			Segment:        seg,
			Fields: map[string]string{
				"track_id":          strconv.FormatUint(uint64(trackID), 10),
				"handler":           track.Handler,
				"timescale":         strconv.FormatUint(uint64(track.Timescale), 10),
				"decode_time_start": strconv.FormatUint(timeline.firstDecodeTime, 10),
				"decode_time_end":   strconv.FormatUint(timeline.endDecodeTime, 10),
				"duration":          strconv.FormatUint(timeline.duration, 10),
				"duration_seconds":  strconv.FormatFloat(durationSeconds, 'f', -1, 64),
				"sample_count":      strconv.Itoa(timeline.sampleCount),
			},
			Timestamp: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *SegmentDecoder) initInfo(ctx context.Context, mapURL string) (fmp4.InitInfo, error) {
	d.initMu.Lock()
	if info, ok := d.initCache[mapURL]; ok {
		d.initMu.Unlock()
		return info, nil
	}
	if fetch := d.initFetching[mapURL]; fetch != nil {
		d.initMu.Unlock()
		select {
		case <-ctx.Done():
			return fmp4.InitInfo{}, ctx.Err()
		case <-fetch.done:
			return fetch.info, fetch.err
		}
	}
	fetch := &initFetch{done: make(chan struct{})}
	d.initFetching[mapURL] = fetch
	d.initMu.Unlock()

	defer func() {
		d.initMu.Lock()
		delete(d.initFetching, mapURL)
		d.initMu.Unlock()
		close(fetch.done)
	}()

	// Bound the init fetch like the media-segment download so a stalled init
	// body cannot hang a decode worker (and, through the results channel, the
	// whole poller) indefinitely.
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	data, err := d.fetchBytes(fetchCtx, mapURL, MaxInitSegmentBytes, "fmp4 init")
	if err != nil {
		fetch.err = fmt.Errorf("fetch fmp4 init: %w", err)
		return fmp4.InitInfo{}, fetch.err
	}
	info, err := fmp4.ParseInit(data)
	if err != nil {
		fetch.err = fmt.Errorf("parse fmp4 init: %w", err)
		return fmp4.InitInfo{}, fetch.err
	}

	d.initMu.Lock()
	d.rememberInit(mapURL, info)
	d.initMu.Unlock()
	fetch.info = info
	return fetch.info, nil
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
