package hls

import (
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

// SegmentDecoder downloads HLS media segments and returns markers found inside.
type SegmentDecoder struct {
	client    *http.Client
	initMu    sync.Mutex
	initCache map[string]fmp4.InitInfo
}

func newSegmentDecoderWithClient(client *http.Client) *SegmentDecoder {
	return &SegmentDecoder{
		client:    client,
		initCache: make(map[string]fmp4.InitInfo),
	}
}

// Decode downloads and decodes one HLS media segment.
func (d *SegmentDecoder) Decode(ctx context.Context, segURL, mapURL string, seg int, emit func(*marker.Marker) error) error {
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	segmentData, err := d.fetchBytes(dlCtx, segURL)
	if err != nil {
		return err
	}

	if mapURL != "" {
		return d.decodeFMP4(ctx, mapURL, segmentData, seg, emit)
	}

	decoder := mpegts.NewDecoder()
	markers, decodeErr := decoder.DecodeBuf(segmentData)
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

	groups, err := id3.ParseFromMPEGTS(segmentData)
	if err != nil {
		return fmt.Errorf("parse id3: %w", err)
	}
	return emitID3Groups(groups, seg, emit)
}

func (d *SegmentDecoder) fetchBytes(ctx context.Context, url string) ([]byte, error) {
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
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxSegmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > MaxSegmentBytes {
		return nil, fmt.Errorf("segment too large: exceeds %d bytes", MaxSegmentBytes)
	}
	return data, nil
}

func (d *SegmentDecoder) decodeFMP4(ctx context.Context, mapURL string, segmentData []byte, seg int, emit func(*marker.Marker) error) error {
	init, err := d.initInfo(ctx, mapURL)
	if err != nil {
		return err
	}
	timing, err := fmp4.ParseFragment(init, segmentData)
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
	d.initMu.Unlock()

	data, err := d.fetchBytes(ctx, mapURL)
	if err != nil {
		return fmp4.InitInfo{}, fmt.Errorf("fetch fmp4 init: %w", err)
	}
	info, err := fmp4.ParseInit(data)
	if err != nil {
		return fmp4.InitInfo{}, fmt.Errorf("parse fmp4 init: %w", err)
	}

	d.initMu.Lock()
	d.initCache[mapURL] = info
	d.initMu.Unlock()
	return info, nil
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
