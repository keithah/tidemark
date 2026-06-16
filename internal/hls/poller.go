package hls

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/keithah/tidemark/internal/httpclient"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/pipeline"
	"github.com/keithah/tidemark/internal/scte35"
)

const (
	MaxManifestBytes      = 1 << 20
	defaultPollInterval   = 2 * time.Second
	minPollInterval       = 250 * time.Millisecond
	maxPollInterval       = 30 * time.Second
	defaultSegmentWorkers = 4
	defaultSeenLimit      = 4096
	maxRetryDelay         = 30 * time.Second
)

// Poller polls an HLS manifest and emits markers for SCTE-35 tags found.
type Poller struct {
	url            string
	errors         io.Writer
	client         *http.Client
	pollInterval   time.Duration
	segmentWorkers int
	planner        playlistPlanner
	validators     map[string]manifestValidator
	segmentDecoder *SegmentDecoder
}

type manifestValidator struct {
	etag             string
	lastModified     string
	lastWaitInterval time.Duration
}

type resolvedManifest struct {
	url     string
	body    string
	hasBody bool
}

var errManifestNotModified = errors.New("manifest not modified")

type permanentHTTPStatusError struct {
	status int
}

func (e permanentHTTPStatusError) Error() string {
	return fmt.Sprintf("permanent HTTP status %d", e.status)
}

// Option configures a Poller.
type Option func(*Poller)

// WithErrorWriter reports recoverable poll/decode errors to w.
func WithErrorWriter(w io.Writer) Option {
	return func(p *Poller) {
		if w != nil {
			p.errors = w
		}
	}
}

// NewPoller creates a new HLS manifest poller.
func NewPoller(url string, opts ...Option) *Poller {
	transport := httpclient.NewTransport()
	client := httpclient.NewStreamingWithTransport(transport)
	p := &Poller{
		url:            url,
		errors:         io.Discard,
		client:         client,
		pollInterval:   defaultPollInterval,
		segmentWorkers: defaultSegmentWorkers,
		planner:        newPlaylistPlanner(defaultSeenLimit),
		validators:     make(map[string]manifestValidator),
		segmentDecoder: newSegmentDecoderWithClient(client),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Poll polls the HLS manifest and emits markers on the channel.
// It blocks until the context is cancelled or the stream ends (VOD with ENDLIST).
func (p *Poller) Poll(ctx context.Context, ch chan<- *marker.Marker) error {
	retryDelay := p.pollInterval

	// Check if this is a master playlist and resolve to media playlist
	resolved, err := p.resolveInitialManifest(ctx, p.url)
	if err != nil {
		return err
	}
	manifestURL := resolved.url
	initialBody := resolved.body
	hasInitialBody := resolved.hasBody

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var endlist bool
		var waitInterval time.Duration
		if hasInitialBody {
			endlist, waitInterval, err = p.processManifest(ctx, manifestURL, initialBody, ch)
			hasInitialBody = false
			initialBody = ""
		} else {
			endlist, waitInterval, err = p.fetchAndProcess(ctx, manifestURL, ch)
		}
		if err != nil {
			var permanent permanentHTTPStatusError
			if errors.As(err, &permanent) {
				return err
			}
			p.reportf("fetch manifest: %v", err)
			if waitErr := waitWithJitter(ctx, retryDelay); waitErr != nil {
				return waitErr
			}
			retryDelay = nextRetryDelay(retryDelay)
			continue
		}
		retryDelay = p.pollInterval

		if endlist {
			return nil // VOD complete
		}

		if err := wait(ctx, waitInterval); err != nil {
			return err
		}
	}
}

func (p *Poller) resolveInitialManifest(ctx context.Context, manifestURL string) (resolvedManifest, error) {
	body, err := p.fetchManifest(ctx, manifestURL)
	if err != nil {
		return resolvedManifest{}, err
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
			return resolvedManifest{url: resolveURL(manifestURL, line)}, nil
		}
	}
	return resolvedManifest{url: manifestURL, body: body, hasBody: true}, nil
}

func (p *Poller) fetchAndProcess(ctx context.Context, manifestURL string, ch chan<- *marker.Marker) (bool, time.Duration, error) {
	body, err := p.fetchManifest(ctx, manifestURL)
	if err != nil {
		if errors.Is(err, errManifestNotModified) {
			return false, p.cachedPollInterval(manifestURL), nil
		}
		return false, p.pollInterval, err
	}
	return p.processManifest(ctx, manifestURL, body, ch)
}

func (p *Poller) processManifest(ctx context.Context, manifestURL, body string, ch chan<- *marker.Marker) (bool, time.Duration, error) {
	playlist := ParsePlaylist(body)
	waitInterval := p.pollIntervalFor(playlist)
	plans, jobs, err := p.planner.plan(manifestURL, playlist)
	if err != nil {
		return false, waitInterval, err
	}

	decodeCtx, cancelDecode := context.WithCancel(ctx)
	defer cancelDecode()
	results := p.decodeSegments(decodeCtx, jobs)
	decoded := make(map[string]segmentResult, len(jobs))
	nextPlan := 0
	flushReady := func() error {
		for nextPlan < len(plans) {
			plan := plans[nextPlan]
			result, hasResult := decoded[plan.url]
			if plan.emitSegment && !hasResult {
				return nil
			}
			for _, tag := range plan.tags {
				if err := p.emitTag(ctx, tag, plan.sequence, ch); err != nil {
					return err
				}
			}
			if plan.emitSegment {
				delete(decoded, plan.url)
				if result.err != nil {
					p.reportf("decode segment %s: %v", result.url, result.err)
				} else {
					for _, m := range result.markers {
						if err := pipeline.SendMarker(ctx, ch, m); err != nil {
							return err
						}
					}
					p.planner.rememberDecodedSegment(plan.url)
				}
			}
			nextPlan++
		}
		return nil
	}

	if err := flushReady(); err != nil {
		return false, waitInterval, err
	}
	for result := range results {
		decoded[result.url] = result
		if err := flushReady(); err != nil {
			return false, waitInterval, err
		}
	}
	if err := flushReady(); err != nil {
		return false, waitInterval, err
	}
	p.rememberPollInterval(manifestURL, waitInterval)
	return playlist.Endlist, waitInterval, nil
}

func (p *Poller) decodeSegments(ctx context.Context, jobs []segmentJob) <-chan segmentResult {
	results := make(chan segmentResult)
	go func() {
		defer close(results)
		if len(jobs) == 0 {
			return
		}
		limit := p.segmentWorkers
		if limit <= 0 {
			limit = 1
		}
		if limit > len(jobs) {
			limit = len(jobs)
		}
		jobCh := make(chan segmentJob)
		var wg sync.WaitGroup
		for i := 0; i < limit; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobCh {
					markers := make([]*marker.Marker, 0, 4)
					err := p.segmentDecoder.Decode(ctx, job.url, job.sequence, func(m *marker.Marker) error {
						markers = append(markers, m)
						return nil
					})
					result := segmentResult{url: job.url, markers: markers, err: err}
					select {
					case <-ctx.Done():
						return
					case results <- result:
					}
				}
			}()
		}
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				close(jobCh)
				wg.Wait()
				return
			case jobCh <- job:
			}
		}
		close(jobCh)
		wg.Wait()
	}()
	return results
}

func (p *Poller) pollIntervalFor(playlist Playlist) time.Duration {
	if p.pollInterval != defaultPollInterval || playlist.TargetDuration <= 0 {
		return p.pollInterval
	}
	if playlist.TargetDuration < minPollInterval {
		return minPollInterval
	}
	if playlist.TargetDuration > maxPollInterval {
		return maxPollInterval
	}
	return playlist.TargetDuration
}

func (p *Poller) cachedPollInterval(manifestURL string) time.Duration {
	if validator, ok := p.validators[manifestURL]; ok && validator.lastWaitInterval > 0 {
		return validator.lastWaitInterval
	}
	return p.pollInterval
}

func (p *Poller) rememberPollInterval(manifestURL string, interval time.Duration) {
	validator := p.validators[manifestURL]
	validator.lastWaitInterval = interval
	p.validators[manifestURL] = validator
}

func (p *Poller) emitTag(ctx context.Context, tag *TagResult, seg int, ch chan<- *marker.Marker) error {
	if tag.IsDirect {
		m := &marker.Marker{
			Type:           marker.MarkerSCTE35,
			Classification: tag.DirectType,
			Source:         "hls_manifest",
			Tag:            tag.Tag,
			Segment:        seg,
			Timestamp:      time.Now(),
		}
		return pipeline.SendMarker(ctx, ch, m)
	}

	m, err := scte35.Decode(tag.Payload, "hls_manifest", tag.Tag)
	if err != nil {
		p.reportf("decode SCTE-35: %v", err)
		return nil
	}
	m.Segment = seg
	m.Timestamp = time.Now()
	return pipeline.SendMarker(ctx, ch, m)
}

func (p *Poller) fetchManifest(ctx context.Context, manifestURL string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", manifestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if validator, ok := p.validators[manifestURL]; ok {
		if validator.etag != "" {
			req.Header.Set("If-None-Match", validator.etag)
		}
		if validator.lastModified != "" {
			req.Header.Set("If-Modified-Since", validator.lastModified)
		}
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer httpclient.DrainAndClose(resp.Body)

	if resp.StatusCode == http.StatusNotModified {
		return "", errManifestNotModified
	}
	if resp.StatusCode != http.StatusOK {
		if isPermanentHTTPStatus(resp.StatusCode) {
			return "", permanentHTTPStatusError{status: resp.StatusCode}
		}
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := readLimited(resp.Body, MaxManifestBytes, "manifest")
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	validator := p.validators[manifestURL]
	validator.etag = resp.Header.Get("ETag")
	validator.lastModified = resp.Header.Get("Last-Modified")
	p.validators[manifestURL] = validator

	return string(data), nil
}

func isPermanentHTTPStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusTooManyRequests
}

func (p *Poller) reportf(format string, args ...interface{}) {
	fmt.Fprintf(p.errors, "[tidemark] error: "+format+"\n", args...)
}

func resolveURL(base, ref string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return resolveRef(baseURL, ref)
}

func resolveRef(baseURL *url.URL, ref string) string {
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(refURL).String()
}

func readLimited(r io.Reader, maxBytes int, label string) ([]byte, error) {
	limited := io.LimitReader(r, int64(maxBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("%s too large: exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitWithJitter(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = defaultPollInterval
	}
	jitterMax := d / 2
	if jitterMax > 0 {
		d += time.Duration(rand.Int63n(int64(jitterMax)))
	}
	return wait(ctx, d)
}

func nextRetryDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultPollInterval
	}
	d *= 2
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}
