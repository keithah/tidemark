package output

import (
	"encoding/json"
	"fmt"
	"os"
	"github.com/keithah/tidemark/internal/marker"
)

// JSONOut writes markers as newline-delimited JSON to a file.
type JSONOut struct {
	f *os.File
}

// NewJSONOut creates or truncates the NDJSON output file.
func NewJSONOut(path string) (*JSONOut, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create json-out: %w", err)
	}
	return &JSONOut{f: f}, nil
}

// Write marshals a marker as a single-line JSON and writes it to the file.
func (j *JSONOut) Write(m *marker.Marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := j.f.Write(data); err != nil {
		return err
	}
	if _, err := j.f.Write([]byte("\n")); err != nil {
		return err
	}
	return j.f.Sync()
}

// Close closes the output file.
func (j *JSONOut) Close() error {
	if j.f != nil {
		return j.f.Close()
	}
	return nil
}
