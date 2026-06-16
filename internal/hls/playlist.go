package hls

import (
	"bufio"
	"strconv"
	"strings"
	"time"
)

// Playlist is the parsed subset of an HLS media playlist used by the poller.
type Playlist struct {
	Endlist        bool
	TargetDuration time.Duration
	Segments       []Segment
}

// Segment represents a media segment and the SCTE-35 tags that apply to it.
type Segment struct {
	Sequence int
	URI      string
	Tags     []*TagResult
}

// ParsePlaylist parses the media playlist structure needed for marker polling.
func ParsePlaylist(body string) Playlist {
	sc := bufio.NewScanner(strings.NewReader(body))
	mediaSeq := 0
	segIdx := 0
	var pendingTags []*TagResult
	var playlist Playlist

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())

		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				mediaSeq = n
			}
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			val := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && n > 0 {
				playlist.TargetDuration = time.Duration(n) * time.Second
			}
			continue
		}

		if line == "#EXT-X-ENDLIST" {
			playlist.Endlist = true
			continue
		}

		if tag := ParseLine(line); tag != nil {
			pendingTags = append(pendingTags, tag)
			continue
		}

		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		tags := pendingTags
		pendingTags = nil
		playlist.Segments = append(playlist.Segments, Segment{
			Sequence: mediaSeq + segIdx,
			URI:      line,
			Tags:     tags,
		})
		segIdx++
	}

	return playlist
}
