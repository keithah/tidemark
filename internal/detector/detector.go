package detector

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"github.com/keithah/tidemark/internal/marker"
)

// Detect probes the given URL and returns the detected stream type.
// It sends an HTTP GET with Icy-MetaData: 1 and inspects the response headers.
// UDP URLs (udp://) are detected without an HTTP request.
func Detect(ctx context.Context, url string) (*marker.DetectResult, error) {
	// Fast path: UDP scheme
	if strings.HasPrefix(url, "udp://") {
		return &marker.DetectResult{Type: marker.StreamUDP}, nil
	}

	// URL suffix heuristics
	lower := strings.ToLower(url)
	if strings.HasSuffix(lower, ".m3u8") || strings.Contains(lower, ".m3u8?") {
		return &marker.DetectResult{Type: marker.StreamHLS}, nil
	}
	if strings.HasSuffix(lower, ".ts") || strings.Contains(lower, ".ts?") {
		return &marker.DetectResult{Type: marker.StreamMPEGTS}, nil
	}
	if strings.Contains(lower, "/hls/") {
		return &marker.DetectResult{Type: marker.StreamHLS}, nil
	}

	// HTTP probe
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Icy-MetaData", "1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check for ICY metaint header
	icyStr := resp.Header.Get("icy-metaint")
	if icyStr != "" {
		metaInt, err := strconv.Atoi(icyStr)
		if err == nil && metaInt > 0 {
			return &marker.DetectResult{Type: marker.StreamICY, MetaInt: metaInt}, nil
		}
	}

	// Check Content-Type
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "mpegurl") || strings.Contains(ct, "x-mpegurl"):
		return &marker.DetectResult{Type: marker.StreamHLS}, nil
	case strings.Contains(ct, "mp2t"):
		return &marker.DetectResult{Type: marker.StreamMPEGTS}, nil
	case strings.Contains(ct, "audio/mpeg") || strings.Contains(ct, "audio/aac"):
		// Audio without ICY header — probe as ICY fallback
		return &marker.DetectResult{Type: marker.StreamICY}, nil
	}

	return &marker.DetectResult{Type: marker.StreamUnknown}, nil
}

// HTTPGet performs a simple HTTP GET request with context support.
// Used by callers that need a raw response body (e.g., MPEGTS stream reading).
func HTTPGet(ctx context.Context, url string) (*http.Response, error) {
	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return resp, nil
}
