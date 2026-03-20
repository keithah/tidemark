package hls

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/keithah/tidemark/internal/classifier"
	"github.com/keithah/tidemark/internal/id3"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/mpegts"
	"github.com/keithah/tidemark/internal/scte35"
)

// Poller polls an HLS manifest and emits markers for SCTE-35 tags found.
type Poller struct {
	url           string
	classify      *classifier.Classifier
	seen          map[int]bool   // seen segment sequence numbers
	seenSegments  map[string]bool // seen segment URIs (for download dedup)
	mpegtsDecoder *mpegts.Decoder
}

// NewPoller creates a new HLS manifest poller.
func NewPoller(url string, cls *classifier.Classifier) *Poller {
	return &Poller{
		url:           url,
		classify:      cls,
		seen:          make(map[int]bool),
		seenSegments:  make(map[string]bool),
		mpegtsDecoder: mpegts.NewDecoder(),
	}
}

// Poll polls the HLS manifest and emits markers on the channel.
// It blocks until the context is cancelled or the stream ends (VOD with ENDLIST).
func (p *Poller) Poll(ctx context.Context, ch chan<- *marker.Marker) error {
	manifestURL := p.url

	// Check if this is a master playlist and resolve to media playlist
	resolved, err := p.resolveMaster(ctx, manifestURL)
	if err != nil {
		return err
	}
	if resolved != "" {
		manifestURL = resolved
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		endlist, err := p.fetchAndProcess(ctx, manifestURL, ch)
		if err != nil {
			fmt.Fprintf(io.Discard, "[tidemark] error: fetch manifest: %v\n", err)
			// Retry on transient errors
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}

		if endlist {
			return nil // VOD complete
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (p *Poller) resolveMaster(ctx context.Context, manifestURL string) (string, error) {
	body, err := p.fetchManifest(ctx, manifestURL)
	if err != nil {
		return "", err
	}

	sc := bufio.NewScanner(strings.NewReader(body))
	isMaster := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			isMaster = true
			continue
		}
		if isMaster && !strings.HasPrefix(line, "#") && line != "" {
			return resolveURL(manifestURL, line), nil
		}
	}
	return "", nil
}

func (p *Poller) fetchAndProcess(ctx context.Context, manifestURL string, ch chan<- *marker.Marker) (bool, error) {
	body, err := p.fetchManifest(ctx, manifestURL)
	if err != nil {
		return false, err
	}

	sc := bufio.NewScanner(strings.NewReader(body))
	mediaSeq := 0
	segIdx := 0
	var pendingTags []*TagResult
	endlist := false

	for sc.Scan() {
		line := sc.Text()

		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				mediaSeq = n
			}
			continue
		}

		if line == "#EXT-X-ENDLIST" {
			endlist = true
			continue
		}

		// Check for SCTE-35 tags
		if tag := ParseLine(line); tag != nil {
			pendingTags = append(pendingTags, tag)
			continue
		}

		// Segment URI line (not a comment, not empty)
		if !strings.HasPrefix(line, "#") && line != "" {
			absSeq := mediaSeq + segIdx
			segIdx++

			// Process pending tags for this segment
			for _, tag := range pendingTags {
				if p.seen[absSeq] {
					continue
				}
				p.emitTag(tag, absSeq, ch)
			}
			pendingTags = nil
			p.seen[absSeq] = true

			// Download segment for MPEGTS + ID3 extraction
			segURL := resolveURL(manifestURL, line)
			if !p.seenSegments[segURL] {
				p.seenSegments[segURL] = true
				p.downloadAndDecode(ctx, segURL, absSeq, ch)
			}
		}
	}

	return endlist, nil
}

func (p *Poller) emitTag(tag *TagResult, seg int, ch chan<- *marker.Marker) {
	if tag.IsDirect {
		m := &marker.Marker{
			Type:           marker.MarkerSCTE35,
			Classification: tag.DirectType,
			Source:         "hls_manifest",
			Tag:           tag.Tag,
			Segment:       seg,
			Timestamp:      time.Now(),
		}
		ch <- m
		return
	}

	m, err := scte35.Decode(tag.Payload, "hls_manifest", tag.Tag)
	if err != nil {
		fmt.Fprintf(io.Discard, "[tidemark] error: decode SCTE-35: %v\n", err)
		return
	}
	m.Segment = seg
	m.Classification = p.classify.Classify(m)
	m.Timestamp = time.Now()
	ch <- m
}

func (p *Poller) downloadAndDecode(ctx context.Context, segURL string, seg int, ch chan<- *marker.Marker) {
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(dlCtx, "GET", segURL, nil)
	if err != nil {
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	// MPEGTS decode
	markers := p.mpegtsDecoder.DecodeBuf(data)
	for _, m := range markers {
		m.Segment = seg
		m.Source = "hls_segment"
		m.Classification = p.classify.Classify(m)
		m.Timestamp = time.Now()
		ch <- m
	}

	// ID3 extraction
	tags, _ := id3.Parse(data)
	for _, tag := range tags {
		m := &marker.Marker{
			Type:      marker.MarkerID3,
			Source:    "hls_segment",
			Segment:  seg,
			Tags:     map[string]string{tag.ID: tag.Value},
			Timestamp: time.Now(),
		}
		m.Classification = p.classify.Classify(m)
		ch <- m
	}
}

func (p *Poller) fetchManifest(ctx context.Context, manifestURL string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	return string(data), nil
}

func resolveURL(base, ref string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(refURL).String()
}
