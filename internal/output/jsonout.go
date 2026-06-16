package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/keithah/tidemark/internal/marker"
)

// JSONOut writes markers as newline-delimited JSON to a file.
type JSONOut struct {
	f   *os.File
	w   *bufio.Writer
	enc *json.Encoder
}

// NewJSONOut creates or truncates the NDJSON output file.
func NewJSONOut(path string) (*JSONOut, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create json-out: %w", err)
	}
	w := bufio.NewWriter(f)
	return &JSONOut{f: f, w: w, enc: json.NewEncoder(w)}, nil
}

// Write marshals a marker as a single-line JSON and writes it to the file.
func (j *JSONOut) Write(m *marker.Marker) error {
	if err := j.enc.Encode(m); err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return nil
}

// Close flushes pending output, syncs it to disk, and closes the output file.
func (j *JSONOut) Close() error {
	if j.f == nil {
		return nil
	}
	var firstErr error
	if j.w != nil {
		if err := j.w.Flush(); err != nil {
			firstErr = err
		}
	}
	if err := j.f.Sync(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := j.f.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	j.f = nil
	j.w = nil
	j.enc = nil
	return firstErr
}
