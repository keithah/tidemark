package hls

import (
	"fmt"
	"net/url"

	"github.com/keithah/tidemark/internal/marker"
)

type playlistPlanner struct {
	tagSeen     *seenWindow
	segmentSeen *seenWindow
	urls        *urlCache
}

func newPlaylistPlanner(limit int) playlistPlanner {
	return playlistPlanner{
		tagSeen:     newSeenWindow(limit),
		segmentSeen: newSeenWindow(limit),
		urls:        newURLCache(limit),
	}
}

func (p *playlistPlanner) plan(manifestURL string, playlist Playlist) ([]segmentPlan, []segmentJob, error) {
	baseURL, err := url.Parse(manifestURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse manifest URL: %w", err)
	}

	plans := make([]segmentPlan, 0, len(playlist.Segments))
	scheduled := make(map[string]struct{}, len(playlist.Segments))
	jobs := make([]segmentJob, 0, len(playlist.Segments))
	for _, segment := range playlist.Segments {
		segURL := p.resolveSegmentURL(manifestURL, baseURL, segment.URI)
		plan := segmentPlan{sequence: segment.Sequence, url: segURL}

		if !p.tagSeen.Has(segURL) {
			plan.tags = segment.Tags
			p.tagSeen.Remember(segURL)
		}
		if _, ok := scheduled[segURL]; !ok && !p.segmentSeen.Has(segURL) {
			jobs = append(jobs, segmentJob{sequence: segment.Sequence, url: segURL})
			plan.emitSegment = true
			scheduled[segURL] = struct{}{}
		}
		plans = append(plans, plan)
	}
	return plans, jobs, nil
}

func (p *playlistPlanner) resolveSegmentURL(manifestURL string, baseURL *url.URL, segmentURI string) string {
	key := manifestURL + "\x00" + segmentURI
	if resolved, ok := p.urls.Get(key); ok {
		return resolved
	}
	resolved := resolveRef(baseURL, segmentURI)
	p.urls.Remember(key, resolved)
	return resolved
}

func (p *playlistPlanner) rememberDecodedSegment(url string) {
	p.segmentSeen.Remember(url)
}

type segmentPlan struct {
	sequence    int
	url         string
	tags        []*TagResult
	emitSegment bool
}

type segmentJob struct {
	sequence int
	url      string
}

type segmentResult struct {
	url     string
	markers []*marker.Marker
	err     error
}

type seenWindow struct {
	limit int
	seen  map[string]struct{}
	order []string
	next  int
}

func newSeenWindow(limit int) *seenWindow {
	if limit <= 0 {
		limit = defaultSeenLimit
	}
	return &seenWindow{
		limit: limit,
		seen:  make(map[string]struct{}, limit),
		order: make([]string, 0, limit),
	}
}

func (w *seenWindow) Has(key string) bool {
	_, ok := w.seen[key]
	return ok
}

func (w *seenWindow) Remember(key string) {
	if _, ok := w.seen[key]; ok {
		return
	}
	if len(w.order) < w.limit {
		w.order = append(w.order, key)
	} else {
		old := w.order[w.next]
		delete(w.seen, old)
		w.order[w.next] = key
		w.next = (w.next + 1) % w.limit
	}
	w.seen[key] = struct{}{}
}

type urlCache struct {
	limit int
	items map[string]string
	order []string
	next  int
}

func newURLCache(limit int) *urlCache {
	if limit <= 0 {
		limit = defaultSeenLimit
	}
	return &urlCache{
		limit: limit,
		items: make(map[string]string, limit),
		order: make([]string, 0, limit),
	}
}

func (c *urlCache) Get(key string) (string, bool) {
	value, ok := c.items[key]
	return value, ok
}

func (c *urlCache) Remember(key, value string) {
	if _, ok := c.items[key]; ok {
		c.items[key] = value
		return
	}
	if len(c.order) < c.limit {
		c.order = append(c.order, key)
	} else {
		old := c.order[c.next]
		delete(c.items, old)
		c.order[c.next] = key
		c.next = (c.next + 1) % c.limit
	}
	c.items[key] = value
}
