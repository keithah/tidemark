package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keithah/tidemark/internal/classifier"
	"github.com/keithah/tidemark/internal/detector"
	"github.com/keithah/tidemark/internal/hls"
	"github.com/keithah/tidemark/internal/icy"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/mpegts"
	"github.com/keithah/tidemark/internal/output"
	"github.com/keithah/tidemark/internal/udp"
)

// Version is set via ldflags at build time.
var Version = "dev"

// Config holds all CLI flag values.
type Config struct {
	NoColor bool
	JSON    bool
	Quiet   bool
	JSONOut string
	Timeout int
	Filter  string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, url, ctx, cancel, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tidemark] error: %s\n", err)
		return 1
	}
	defer cancel()

	if url == "" {
		fmt.Fprintf(os.Stderr, "[tidemark] error: URL argument required\n")
		fmt.Fprintf(os.Stderr, "Usage: tidemark <url> [flags]\n")
		return 1
	}

	// Detect stream type
	result, err := detector.Detect(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tidemark] error: %s\n", err)
		return 1
	}

	printBanner(os.Stderr, url, result.Type, cfg)

	cls := classifier.New()
	ch := make(chan *marker.Marker, 16)

	var runErr error
	switch result.Type {
	case marker.StreamICY:
		runErr = runICY(ctx, url, result.MetaInt, cls, ch, cfg)
	case marker.StreamHLS:
		runErr = runHLS(ctx, url, cls, ch, cfg)
	case marker.StreamMPEGTS:
		runErr = runMPEGTS(ctx, url, cls, ch, cfg)
	case marker.StreamUDP:
		runErr = runUDP(ctx, url, cls, ch, cfg)
	default:
		fmt.Fprintf(os.Stderr, "[tidemark] stream type %s not yet supported\n", result.Type)
		return 1
	}

	if runErr != nil && runErr != context.Canceled && runErr != context.DeadlineExceeded {
		fmt.Fprintf(os.Stderr, "[tidemark] error: %s\n", runErr)
		return 1
	}
	return 0
}

func parseFlags(args []string) (*Config, string, context.Context, context.CancelFunc, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("tidemark", flag.ContinueOnError)
	fs.BoolVar(&cfg.NoColor, "no-color", false, "Disable ANSI color output")
	fs.BoolVar(&cfg.JSON, "json", false, "Machine-readable JSON output only")
	fs.BoolVar(&cfg.Quiet, "quiet", false, "Summary lines only, suppress JSON blocks")
	fs.StringVar(&cfg.JSONOut, "json-out", "", "Write all marker JSON to FILE (NDJSON)")
	fs.IntVar(&cfg.Timeout, "timeout", 0, "Stop after N seconds (0=run until Ctrl+C)")
	fs.StringVar(&cfg.Filter, "filter", "", "Only show markers of type: scte35 | id3 | icy")

	fs.BoolVar(&cfg.NoColor, "version", false, "")
	if err := fs.Parse(args); err != nil {
		return nil, "", nil, func() {}, err
	}

	// Check for --version
	// (Reuse the parsed flag as a hack — better to check args directly)
	for _, a := range args {
		if a == "--version" || a == "-version" {
			fmt.Printf("tidemark %s\n", Version)
			os.Exit(0)
		}
	}

	// Reset NoColor in case --version set it
	cfg.NoColor = false
	for _, a := range args {
		if a == "--no-color" || a == "-no-color" {
			cfg.NoColor = true
		}
	}

	// Validate
	if cfg.JSON && cfg.Quiet {
		return nil, "", nil, func() {}, fmt.Errorf("--json and --quiet are mutually exclusive")
	}

	if cfg.Filter != "" {
		f := strings.ToLower(cfg.Filter)
		switch f {
		case "scte35", "id3", "icy":
			cfg.Filter = f
		default:
			return nil, "", nil, func() {}, fmt.Errorf("--filter must be one of: scte35, id3, icy")
		}
	}

	url := fs.Arg(0)

	// Context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	if cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Timeout)*time.Second)
	}

	return cfg, url, ctx, cancel, nil
}

func printBanner(w io.Writer, url string, streamType marker.StreamType, cfg *Config) {
	fmt.Fprintf(w, "[tidemark] url:    %s\n", url)
	fmt.Fprintf(w, "[tidemark] type:   %s\n", streamType)
	filter := "all"
	if cfg.Filter != "" {
		filter = cfg.Filter
	}
	fmt.Fprintf(w, "[tidemark] filter: %s\n", filter)
	mode := "default"
	if cfg.JSON {
		mode = "json"
	} else if cfg.Quiet {
		mode = "quiet"
	}
	fmt.Fprintf(w, "[tidemark] output: %s\n", mode)
	if cfg.JSONOut != "" {
		fmt.Fprintf(w, "[tidemark] json-out: %s\n", cfg.JSONOut)
	}
	fmt.Fprintln(w, "─────────────────────────────────────────")
}

func outputConfig(cfg *Config) output.OutputConfig {
	mode := output.ModeDefault
	if cfg.JSON {
		mode = output.ModeJSON
	} else if cfg.Quiet {
		mode = output.ModeQuiet
	}
	return output.OutputConfig{Mode: mode, NoColor: cfg.NoColor}
}

func shouldFilter(m *marker.Marker, filter string) bool {
	if filter == "" {
		return false
	}
	return strings.ToLower(m.Type.String()) != filter
}

func openJSONOut(path string) (*output.JSONOut, error) {
	if path == "" {
		return nil, nil
	}
	return output.NewJSONOut(path)
}

func runICY(ctx context.Context, url string, metaInt int, cls *classifier.Classifier, ch chan *marker.Marker, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading ICY stream...\n")
	ocfg := outputConfig(cfg)
	jout, err := openJSONOut(cfg.JSONOut)
	if err != nil {
		return err
	}

	reader := icy.NewReader(url, metaInt)

	go func() {
		defer close(ch)
		_ = reader.Read(ctx, ch)
	}()

	markerCount := 0
	for m := range ch {
		m.Classification = cls.Classify(m)
		if shouldFilter(m, cfg.Filter) {
			continue
		}
		markerCount++
		if err := output.Print(os.Stdout, m, ocfg); err != nil {
			fmt.Fprintf(os.Stderr, "[tidemark] output error: %s\n", err)
		}
		if jout != nil {
			if err := jout.Write(m); err != nil {
				fmt.Fprintf(os.Stderr, "[tidemark] json-out error: %s\n", err)
			}
		}
	}

	if jout != nil {
		jout.Close()
	}
	fmt.Fprintf(os.Stderr, "\n[tidemark] stopped. %d markers detected.\n", markerCount)
	return nil
}

func runHLS(ctx context.Context, url string, cls *classifier.Classifier, ch chan *marker.Marker, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] polling HLS manifest...\n")
	ocfg := outputConfig(cfg)
	jout, err := openJSONOut(cfg.JSONOut)
	if err != nil {
		return err
	}

	poller := hls.NewPoller(url, cls)

	go func() {
		defer close(ch)
		_ = poller.Poll(ctx, ch)
	}()

	markerCount := 0
	for m := range ch {
		if shouldFilter(m, cfg.Filter) {
			continue
		}
		markerCount++
		if err := output.Print(os.Stdout, m, ocfg); err != nil {
			fmt.Fprintf(os.Stderr, "[tidemark] output error: %s\n", err)
		}
		if jout != nil {
			if err := jout.Write(m); err != nil {
				fmt.Fprintf(os.Stderr, "[tidemark] json-out error: %s\n", err)
			}
		}
	}

	if jout != nil {
		jout.Close()
	}
	fmt.Fprintf(os.Stderr, "\n[tidemark] stopped. %d markers detected.\n", markerCount)
	return nil
}

func runMPEGTS(ctx context.Context, url string, cls *classifier.Classifier, ch chan *marker.Marker, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading MPEGTS stream...\n")
	ocfg := outputConfig(cfg)
	jout, err := openJSONOut(cfg.JSONOut)
	if err != nil {
		return err
	}

	decoder := mpegts.NewDecoder()

	go func() {
		defer close(ch)
		resp, err := detector.HTTPGet(ctx, url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[tidemark] error: %s\n", err)
			return
		}
		defer resp.Body.Close()
		_ = decoder.DecodeReader(ctx, resp.Body, ch)
	}()

	markerCount := 0
	for m := range ch {
		m.Classification = cls.Classify(m)
		if shouldFilter(m, cfg.Filter) {
			continue
		}
		markerCount++
		if err := output.Print(os.Stdout, m, ocfg); err != nil {
			fmt.Fprintf(os.Stderr, "[tidemark] output error: %s\n", err)
		}
		if jout != nil {
			if err := jout.Write(m); err != nil {
				fmt.Fprintf(os.Stderr, "[tidemark] json-out error: %s\n", err)
			}
		}
	}

	if jout != nil {
		jout.Close()
	}
	fmt.Fprintf(os.Stderr, "\n[tidemark] stopped. %d markers detected.\n", markerCount)
	return nil
}

func runUDP(ctx context.Context, url string, cls *classifier.Classifier, ch chan *marker.Marker, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading UDP stream...\n")
	ocfg := outputConfig(cfg)
	jout, err := openJSONOut(cfg.JSONOut)
	if err != nil {
		return err
	}

	reader := udp.NewReader(url)

	go func() {
		defer close(ch)
		_ = reader.Read(ctx, ch)
	}()

	markerCount := 0
	for m := range ch {
		m.Classification = cls.Classify(m)
		if shouldFilter(m, cfg.Filter) {
			continue
		}
		markerCount++
		if err := output.Print(os.Stdout, m, ocfg); err != nil {
			fmt.Fprintf(os.Stderr, "[tidemark] output error: %s\n", err)
		}
		if jout != nil {
			if err := jout.Write(m); err != nil {
				fmt.Fprintf(os.Stderr, "[tidemark] json-out error: %s\n", err)
			}
		}
	}

	if jout != nil {
		jout.Close()
	}
	fmt.Fprintf(os.Stderr, "\n[tidemark] stopped. %d markers detected.\n", markerCount)
	return nil
}
