package detector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/keithah/tidemark/internal/httpclient"
	"github.com/keithah/tidemark/internal/marker"
)

const sniffBytes = 2048

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

	client := httpclient.NewTimed(10 * time.Second)
	method := http.MethodHead
	resp, err := probeHTTP(ctx, client, url, http.MethodHead)
	if err != nil {
		return nil, fmt.Errorf("probe URL: %w", err)
	}
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		httpclient.DrainAndClose(resp.Body)
		method = http.MethodGet
		resp, err = probeHTTP(ctx, client, url, http.MethodGet)
		if err != nil {
			return nil, fmt.Errorf("probe URL: %w", err)
		}
	}
	defer httpclient.DrainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe URL: status %d", resp.StatusCode)
	}

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

	if method == http.MethodGet {
		if typ, err := sniffResponse(resp.Body); err != nil {
			return nil, fmt.Errorf("sniff URL: %w", err)
		} else if typ != marker.StreamUnknown {
			return &marker.DetectResult{Type: typ}, nil
		}
	} else if typ, err := sniffHTTP(ctx, client, url); err != nil {
		return nil, fmt.Errorf("sniff URL: %w", err)
	} else if typ != marker.StreamUnknown {
		return &marker.DetectResult{Type: typ}, nil
	}

	return &marker.DetectResult{Type: marker.StreamUnknown}, nil
}

func probeHTTP(ctx context.Context, client *http.Client, url, method string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Icy-MetaData", "1")
	return client.Do(req)
}

func sniffHTTP(ctx context.Context, client *http.Client, url string) (marker.StreamType, error) {
	resp, err := probeHTTP(ctx, client, url, http.MethodGet)
	if err != nil {
		return marker.StreamUnknown, err
	}
	defer httpclient.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return marker.StreamUnknown, fmt.Errorf("status %d", resp.StatusCode)
	}
	return sniffResponse(resp.Body)
}

func sniffResponse(r io.Reader) (marker.StreamType, error) {
	data, err := io.ReadAll(io.LimitReader(r, sniffBytes))
	if err != nil {
		return marker.StreamUnknown, err
	}
	return sniffBytesType(data), nil
}

func sniffBytesType(data []byte) marker.StreamType {
	text := strings.TrimLeft(string(data), "\ufeff \t\r\n")
	if strings.HasPrefix(text, "#EXTM3U") {
		return marker.StreamHLS
	}
	for offset := 0; offset < 188 && offset+188 < len(data); offset++ {
		if data[offset] == 0x47 && data[offset+188] == 0x47 {
			return marker.StreamMPEGTS
		}
	}
	return marker.StreamUnknown
}

// HTTPGet performs a simple HTTP GET request with context support.
// Used by callers that need a raw response body (e.g., MPEGTS stream reading).
func HTTPGet(ctx context.Context, url string) (*http.Response, error) {
	client := httpclient.NewStreaming()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, permanentError{err: fmt.Errorf("create request: %w", err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		httpclient.DrainAndClose(resp.Body)
		err := fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, permanentError{err: err}
		}
		return nil, err
	}
	return resp, nil
}

type permanentError struct {
	err error
}

func (e permanentError) Error() string {
	return e.err.Error()
}

func (e permanentError) Unwrap() error {
	return e.err
}

func (e permanentError) Permanent() bool {
	return true
}
