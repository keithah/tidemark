package hls

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/keithah/tidemark/internal/marker"
)

type playlistPlanner struct {
	tagSeen        *boundedMap[tagSeenKey, struct{}]
	segmentSeen    *boundedMap[seenKey, struct{}]
	urls           *boundedMap[string, string]
	epoch          int
	lastFirst      int
	haveFirst      bool
	regressedFirst int
	haveRegression bool
}

type seenKey struct {
	epoch int
	url   string
}

type tagSeenKey struct {
	seenKey
	tag        string
	payload    string
	isDirect   bool
	directType marker.Classification
	attrs      string
	trailing   bool
}

func newPlaylistPlanner(limit int) playlistPlanner {
	return playlistPlanner{
		tagSeen:     newBoundedMap[tagSeenKey, struct{}](limit),
		segmentSeen: newBoundedMap[seenKey, struct{}](limit),
		urls:        newBoundedMap[string, string](limit),
	}
}

func (p *playlistPlanner) plan(manifestURL string, playlist Playlist) ([]segmentPlan, []segmentJob, error) {
	baseURL, err := url.Parse(manifestURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse manifest URL: %w", err)
	}
	p.rememberPlaylistEpoch(playlist)

	plans := make([]segmentPlan, 0, len(playlist.Segments))
	scheduled := make(map[seenKey]struct{}, len(playlist.Segments))
	jobs := make([]segmentJob, 0, len(playlist.Segments))
	for _, segment := range playlist.Segments {
		segURL := p.resolveSegmentURL(manifestURL, baseURL, segment.URI)
		seenKey := p.seenKey(segURL)
		plan := segmentPlan{sequence: segment.Sequence, url: segURL}
		mapURL := ""
		if segment.MapURI != "" {
			mapURL = p.resolveSegmentURL(manifestURL, baseURL, segment.MapURI)
		}

		for _, tag := range segment.Tags {
			tagKey := makeTagSeenKey(seenKey, tag)
			if !p.tagSeen.Has(tagKey) {
				plan.tags = append(plan.tags, tag)
				p.tagSeen.Remember(tagKey, struct{}{})
			}
		}
		if _, ok := scheduled[seenKey]; !ok && !p.segmentSeen.Has(seenKey) {
			jobs = append(jobs, segmentJob{sequence: segment.Sequence, url: segURL, mapURL: mapURL, seenKey: seenKey})
			plan.emitSegment = true
			scheduled[seenKey] = struct{}{}
		}
		plans = append(plans, plan)
	}
	return plans, jobs, nil
}

func (p *playlistPlanner) rememberPlaylistEpoch(playlist Playlist) {
	if len(playlist.Segments) == 0 {
		return
	}
	first := playlist.Segments[0].Sequence
	if p.haveFirst && first < p.lastFirst {
		if !p.haveRegression || p.regressedFirst != first {
			p.regressedFirst = first
			p.haveRegression = true
			return
		}
		p.epoch++
		p.haveRegression = false
		p.regressedFirst = 0
		p.lastFirst = first
		return
	}
	p.haveRegression = false
	p.regressedFirst = 0
	p.lastFirst = first
	p.haveFirst = true
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

func (p *playlistPlanner) seenKey(url string) seenKey {
	return seenKey{epoch: p.epoch, url: url}
}

func (p *playlistPlanner) rememberDecodedSegment(key seenKey) {
	p.segmentSeen.Remember(key, struct{}{})
}

func makeTagSeenKey(key seenKey, tag *TagResult) tagSeenKey {
	if tag == nil {
		return tagSeenKey{seenKey: key}
	}
	if tag.Trailing {
		key.url = ""
	}
	return tagSeenKey{
		seenKey:    key,
		tag:        tag.Tag,
		payload:    tag.Payload,
		isDirect:   tag.IsDirect,
		directType: tag.DirectType,
		attrs:      tagAttributeIdentity(tag.Attributes),
		trailing:   tag.Trailing,
	}
}

func tagAttributeIdentity(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(attrs[key])
		b.WriteByte('\n')
	}
	return b.String()
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
	mapURL   string
	seenKey  seenKey
}

type segmentResult struct {
	url     string
	seenKey seenKey
	markers []*marker.Marker
	err     error
}

type boundedMap[K comparable, V any] struct {
	limit int
	items map[K]V
	order []K
	next  int
}

func newBoundedMap[K comparable, V any](limit int) *boundedMap[K, V] {
	if limit <= 0 {
		limit = defaultSeenLimit
	}
	return &boundedMap[K, V]{
		limit: limit,
		items: make(map[K]V, limit),
		order: make([]K, 0, limit),
	}
}

func (c *boundedMap[K, V]) Get(key K) (V, bool) {
	value, ok := c.items[key]
	return value, ok
}

func (c *boundedMap[K, V]) Has(key K) bool {
	_, ok := c.items[key]
	return ok
}

func (c *boundedMap[K, V]) Remember(key K, value V) {
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
