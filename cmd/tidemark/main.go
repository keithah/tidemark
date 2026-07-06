package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keithah/tidemark/internal/backoff"
	"github.com/keithah/tidemark/internal/classifier"
	"github.com/keithah/tidemark/internal/detector"
	"github.com/keithah/tidemark/internal/hls"
	"github.com/keithah/tidemark/internal/httpclient"
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
	NoColor    bool
	JSON       bool
	Quiet      bool
	Version    bool
	JSONOut    string
	Timeout    int
	MaxRetries int
	Filter     string
	FilterType marker.MarkerType
	HasFilter  bool
}

type markerProducer func(context.Context, chan<- *marker.Marker) error

const (
	sourceInitialRetryDelay = time.Second
	sourceMaxRetryDelay     = 30 * time.Second
	defaultMaxRetries       = 10
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, url, ctx, cancel, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage was already printed by the flag package; -h is not an error.
			return 0
		}
		fmt.Fprintf(os.Stderr, "[tidemark] error: %s\n", err)
		return 1
	}
	defer cancel()

	if cfg.Version {
		fmt.Printf("tidemark %s\n", Version)
		return 0
	}

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

	var runErr error
	switch result.Type {
	case marker.StreamICY:
		runErr = runICY(ctx, url, result.MetaInt, cfg)
	case marker.StreamHLS:
		runErr = runHLS(ctx, url, cfg)
	case marker.StreamMPEGTS:
		runErr = runMPEGTS(ctx, url, cfg)
	case marker.StreamUDP:
		runErr = runUDP(ctx, url, cfg)
	default:
		fmt.Fprintf(os.Stderr, "[tidemark] stream type %s not yet supported\n", result.Type)
		return 1
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
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
	fs.IntVar(&cfg.MaxRetries, "max-retries", defaultMaxRetries, "Max consecutive transient source failures before exiting (0=retry forever)")
	fs.StringVar(&cfg.Filter, "filter", "", "Only show markers of type: scte35 | id3 | icy | fmp4")
	fs.BoolVar(&cfg.Version, "version", false, "Print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, "", nil, func() {}, err
	}

	if fs.NArg() > 1 {
		for _, arg := range fs.Args()[1:] {
			if strings.HasPrefix(arg, "-") {
				return nil, "", nil, func() {}, fmt.Errorf("flags must appear before URL: %s", arg)
			}
		}
		return nil, "", nil, func() {}, fmt.Errorf("unexpected argument: %s", fs.Arg(1))
	}

	// Validate
	if cfg.JSON && cfg.Quiet {
		return nil, "", nil, func() {}, fmt.Errorf("--json and --quiet are mutually exclusive")
	}
	if cfg.Timeout < 0 {
		return nil, "", nil, func() {}, fmt.Errorf("--timeout must be >= 0")
	}
	if cfg.MaxRetries < 0 {
		return nil, "", nil, func() {}, fmt.Errorf("--max-retries must be >= 0")
	}

	if cfg.Filter != "" {
		cfg.Filter = strings.ToLower(cfg.Filter)
		filterType, ok := marker.ParseType(cfg.Filter)
		if !ok {
			return nil, "", nil, func() {}, fmt.Errorf("--filter must be one of: scte35, id3, icy, fmp4")
		}
		cfg.HasFilter = true
		cfg.FilterType = filterType
	}

	url := fs.Arg(0)

	// Context with signal handling
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	ctx := sigCtx
	cancel := stopSignals
	if cfg.Timeout > 0 {
		timeoutCtx, stopTimeout := context.WithTimeout(sigCtx, time.Duration(cfg.Timeout)*time.Second)
		ctx = timeoutCtx
		cancel = func() {
			stopTimeout()
			stopSignals()
		}
	}

	return cfg, url, ctx, cancel, nil
}

func printBanner(w io.Writer, url string, streamType marker.StreamType, cfg *Config) {
	fmt.Fprintf(w, "[tidemark] url:    %s\n", url)
	fmt.Fprintf(w, "[tidemark] type:   %s\n", streamType)
	filter := "all"
	if cfg.HasFilter {
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
	if cfg.MaxRetries > 0 {
		fmt.Fprintf(w, "[tidemark] max retries: %d\n", cfg.MaxRetries)
	} else {
		fmt.Fprintln(w, "[tidemark] max retries: forever")
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

func shouldFilter(m *marker.Marker, cfg *Config) bool {
	if !cfg.HasFilter {
		return false
	}
	return m.Type != cfg.FilterType
}

func openJSONOut(path string) (*output.JSONOut, error) {
	if path == "" {
		return nil, nil
	}
	return output.NewJSONOut(path)
}

func runMarkerSource(ctx context.Context, cfg *Config, producer markerProducer) error {
	sourceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ocfg := outputConfig(cfg)
	jout, err := openJSONOut(cfg.JSONOut)
	if err != nil {
		return err
	}

	cls := classifier.New()
	ch := make(chan *marker.Marker, 16)
	errCh := make(chan error, 1)
	stdout := bufio.NewWriterSize(os.Stdout, 32*1024)

	go func() {
		defer close(ch)
		defer func() {
			if recovered := recover(); recovered != nil {
				errCh <- fmt.Errorf("source panic: %v", recovered)
			}
		}()
		errCh <- producer(sourceCtx, ch)
	}()

	markerCount := 0
	headerPrinted := false
	var outputErr error
	for m := range ch {
		if m.Classification == marker.Unknown {
			m.Classification = cls.Classify(m)
		}
		if jout != nil {
			if err := jout.Write(m); err != nil {
				outputErr = fmt.Errorf("write json-out: %w", err)
				cancel()
				break
			}
		}
		if shouldFilter(m, cfg) {
			continue
		}
		if !headerPrinted {
			if err := output.PrintHeader(stdout, ocfg); err != nil {
				outputErr = fmt.Errorf("output marker: %w", err)
				cancel()
				break
			}
			headerPrinted = true
		}
		markerCount++
		if err := output.Print(stdout, m, ocfg); err != nil {
			outputErr = fmt.Errorf("output marker: %w", err)
			cancel()
			break
		}
		if err := stdout.Flush(); err != nil {
			outputErr = fmt.Errorf("output marker: %w", err)
			cancel()
			break
		}
	}

	fmt.Fprintf(os.Stderr, "\n[tidemark] stopped. %d markers detected.\n", markerCount)
	err = <-errCh
	if flushErr := stdout.Flush(); flushErr != nil && outputErr == nil {
		outputErr = fmt.Errorf("output marker: %w", flushErr)
	}
	if jout != nil {
		if closeErr := jout.Close(); closeErr != nil && outputErr == nil {
			outputErr = fmt.Errorf("close json-out: %w", closeErr)
		}
	}
	if outputErr != nil {
		return outputErr
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return nil
	}
	return err
}

func retryingProducer(producer markerProducer, initialDelay, maxDelay time.Duration) markerProducer {
	return retryingProducerWithPolicy(producer, retryPolicy{
		InitialDelay: initialDelay,
		MaxDelay:     maxDelay,
		MaxFailures:  defaultMaxRetries,
		ErrorWriter:  os.Stderr,
	})
}

func retryingProducerWithErrorWriter(producer markerProducer, initialDelay, maxDelay time.Duration, errw io.Writer) markerProducer {
	return retryingProducerWithPolicy(producer, retryPolicy{
		InitialDelay: initialDelay,
		MaxDelay:     maxDelay,
		ErrorWriter:  errw,
	})
}

type retryPolicy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxFailures  int
	ErrorWriter  io.Writer
}

func retryingProducerWithPolicy(producer markerProducer, policy retryPolicy) markerProducer {
	return func(ctx context.Context, ch chan<- *marker.Marker) error {
		delay := policy.InitialDelay
		failures := 0
		for {
			err := producer(ctx, ch)
			if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if isPermanentError(err) {
				return err
			}
			failures++
			if policy.MaxFailures > 0 && failures >= policy.MaxFailures {
				if policy.ErrorWriter != nil {
					fmt.Fprintf(policy.ErrorWriter, "[tidemark] source error: %v; giving up after %d consecutive failures\n", err, failures)
				}
				return err
			}
			if policy.ErrorWriter != nil {
				fmt.Fprintf(policy.ErrorWriter, "[tidemark] source error: %v; retrying in %s\n", err, delay)
			}
			if delay <= 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				delay = backoff.Next(delay, sourceInitialRetryDelay, policy.MaxDelay)
				continue
			}
			timer := time.NewTimer(backoff.WithJitter(delay, rand.Int63n))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			delay = backoff.Next(delay, sourceInitialRetryDelay, policy.MaxDelay)
		}
	}
}

type permanentError interface {
	Permanent() bool
}

func isPermanentError(err error) bool {
	var perr permanentError
	return errors.As(err, &perr) && perr.Permanent()
}

func runICY(ctx context.Context, url string, metaInt int, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading ICY stream...\n")
	reader := icy.NewReader(url, metaInt)
	return runMarkerSource(ctx, cfg, retryingProducerWithPolicy(reader.Read, sourceRetryPolicy(cfg.MaxRetries)))
}

func runHLS(ctx context.Context, url string, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] polling HLS manifest...\n")
	poller := hls.NewPoller(url, hls.WithErrorWriter(os.Stderr), hls.WithMaxConsecutiveFailures(cfg.MaxRetries))
	return runMarkerSource(ctx, cfg, poller.Poll)
}

func runMPEGTS(ctx context.Context, url string, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading MPEGTS stream...\n")
	decoder := mpegts.NewDecoder()
	return runMarkerSource(ctx, cfg, retryingProducerWithPolicy(func(ctx context.Context, ch chan<- *marker.Marker) error {
		resp, err := detector.HTTPGet(ctx, url)
		if err != nil {
			return err
		}
		resp.Body = httpclient.WithIdleReadTimeout(resp.Body, httpclient.DefaultIdleReadTimeout)
		defer resp.Body.Close()
		return decoder.DecodeReader(ctx, resp.Body, ch)
	}, sourceRetryPolicy(cfg.MaxRetries)))
}

func runUDP(ctx context.Context, url string, cfg *Config) error {
	fmt.Fprintf(os.Stderr, "[tidemark] reading UDP stream...\n")
	reader := udp.NewReader(url)
	return runMarkerSource(ctx, cfg, retryingProducerWithPolicy(reader.Read, sourceRetryPolicy(cfg.MaxRetries)))
}

func sourceRetryPolicy(maxFailures int) retryPolicy {
	return retryPolicy{
		InitialDelay: sourceInitialRetryDelay,
		MaxDelay:     sourceMaxRetryDelay,
		MaxFailures:  maxFailures,
		ErrorWriter:  os.Stderr,
	}
}
