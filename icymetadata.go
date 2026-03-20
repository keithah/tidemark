//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/panjf2000/ants/v2"

	"github.com/tunein/go-common/v12/pkg/data/go-config"
	"github.com/tunein/go-common/v12/pkg/data/text/nlp"
)

const (
	// defaultIcyMetaInt is the icecast server default interval for metadata
	defaultIcyMetaInt      = 16000
	defaultMaxAdDetections = 20
	defaultNoAdTimeout     = time.Hour
	defaultNoIcyTimeout    = 10 * time.Minute
	defaultHTTPTimeout     = 10 * time.Second
	tuneURITemplate        = "https://opml.radiotime.com/Tune.ashx?id=%s&partnerId=%s&render=json"
	tuneURITemplateStage   = "https://stage-opml.radiotime.com/Tune.ashx?id=%s&partnerId=%s&render=json"
)

// Options for running the tool
//
//nolint:lll
type Options struct {
	URL             string        `from:"*" help:"Stream URL or MP3 file path."`
	URLList         string        `from:"*" help:"Filename for URL List - one URL or filename per line."`
	MatchTerms      string        `from:"*" help:"CSV of strings to match to find ad markers in the URLList."`
	IcyMetaInt      int           `from:"*" help:"icy-metaint offset between icy metadata"`
	Recording       string        `from:"*" help:"Filename to record MP3 output (optional)."`
	DontRecordIcy   bool          `from:"*" help:"Don't store icy metadata in recording; prevents playback artifacts when listening to recording."`
	Debug           bool          `from:"*" help:"Enable debug output."`
	Stage           bool          `from:"*" help:"Use staging backend for Tune requests (instead of prod)."`
	FullIcy         bool          `from:"*" help:"Dump full icy metadata (not just StreamTitle)."`
	AddTimestamps   bool          `from:"*" help:"Insert icy timestamps into the recording."`
	MaxAdDetections int           `from:"*" help:"Max successful ad marker detections before stopping this stream."`
	NoAdTimeout     time.Duration `from:"*" help:"Max time to look for ad markers before giving up on this stream."`
	NoIcyTimeout    time.Duration `from:"*" help:"Max time to look for any icy metadata before giving up on this stream."`
	Parallelism     int           `from:"*" help:"Max simultaneous goroutines when running from a URLList."`
	PartnerID       string        `from:"*" help:"Device partner ID for Tune request; use to include ad query params. ex: '!EALLOjB'"`
	StationID       string        `from:"*" help:"RadioMill stationID for Tune request; use to include ad query params. ex: 's297990'"`
	HTTPTimeout     time.Duration `from:"*" help:"Timeout for all outgoing HTTP requests (default=10s)"`
	matchTerms      []string      `from:"-"` // internal use
}

type Icy struct {
	Station
	isFile     bool
	fileSize   int64
	icyMetaInt int
	numAds     int
	prevTitle  string
	prevIcy    string
	foundIcy   bool
	opts       *Options
}

type Station struct {
	url      string
	csvEntry string
}

// StreamTitle get the current song/show in an Icecast stream
func StreamTitle(meta []byte) (string, error) {
	const prefix = "StreamTitle='"
	// Should be at least "StreamTitle=''"
	if len(meta) < len(prefix)+1 {
		return "", fmt.Errorf("no StreamTitle in icy meta (metadata too short)")
	}
	// Split meta by ';', trim it and search for StreamTitle
	for _, m := range bytes.Split(meta, []byte(";")) {
		m = bytes.Trim(m, " \t")
		if !strings.HasPrefix(string(m), prefix) {
			continue
		}
		// take everything after the prefix but before the closing single quote
		return string(m[len(prefix) : len(m)-1]), nil
	}
	return "", fmt.Errorf("no StreamTitle in icy meta (key not found)")
}

func icyToMap(icy string) map[string]string {
	m := map[string]string{}
	kvs := strings.Split(icy, ";")
	for _, kv := range kvs {
		pair := strings.Split(kv, "=")
		if len(pair) != 2 {
			continue
		}
		key := strings.TrimSpace(pair[0])
		value := strings.TrimSpace(pair[1])
		m[key] = value
	}
	return m
}

func findMatches(terms []string, icy string, station Station) bool {
	matches := 0
	m := icyToMap(icy)

	for _, needle := range terms {
		for k, v := range m {
			if strings.Contains(nlp.Normalize(k), needle) {
				matches++
			}
			f := strings.Fields(nlp.Normalize(v))
			for _, field := range f {
				if field == needle {
					matches++
				}
			}
		}
	}
	if matches > 0 {
		fmt.Printf("%s contains ad marker terms in '%s'\n", station.csvEntry, nlp.Normalize(icy))
		return true
	}
	return false
}

func (i *Icy) logIcy(icy []byte, start time.Time, sb int, filePercent float64) {
	i.prevIcy = sanitizeIcyMetadata(icy)
	if i.opts.FullIcy {
		fmt.Printf("  %s icy={%s} sb=%d size=%d\n", time.Now().Format("2006-01-02 15:04:05"), i.prevIcy, sb, sb*16)
	}
	if i.prevIcy != "" {
		i.foundIcy = true
	}
	if i.opts.MatchTerms != "" {
		hasAds := findMatches(i.opts.matchTerms, i.prevIcy, i.Station)
		if hasAds {
			i.numAds++
		}
		return
	}
	newTitle, err := StreamTitle(icy)
	if err != nil && i.opts.AddTimestamps {
		return
	} else if err != nil {
		fmt.Printf("%s offset=%s error with Streamtitle {%s}\n", time.Now(), time.Since(start), err)
		return
	}

	if newTitle == i.prevTitle {
		return
	}

	if i.isFile {
		// stream is a local file, time-based offsets will not be meaningful
		fmt.Printf("offset=%.1f%% StreamTitle={%s}\n", filePercent, newTitle)
	} else {
		// stream is a live URL
		fmt.Printf("%s offset=%s StreamTitle={%s}\n", time.Now(), time.Since(start), newTitle)
	}
	i.prevTitle = newTitle
}

// sanitizeIcyMetadata removes non-printable characters and ensures the metadata is valid UTF-8
func sanitizeIcyMetadata(icy []byte) string {
	// First try to decode as UTF-8
	if utf8.Valid(icy) {
		// Remove non-printable characters
		var result strings.Builder
		for _, r := range string(icy) {
			if unicode.IsPrint(r) {
				result.WriteRune(r)
			}
		}
		return result.String()
	}

	// If not valid UTF-8, return a placeholder
	return "[binary data]"
}

func bool2int(b bool) int {
	if b {
		return 1
	}
	return 0
}

// this will be added in golang 1.17
func unixMicro(t time.Time) int64 {
	return t.Unix()*1e6 + int64(t.Nanosecond())/1e3
}

func padIcyString(s string) string {
	// icy string lengths must be divisible by 16 so we need to pad with null characters
	if len(s)%16 == 0 {
		return s
	}
	pads := 16 - len(s)%16
	for i := 0; i < pads; i++ {
		s += "\000"
	}
	return s
}

func appendTimestamp(icy []byte) []byte {
	// generate timestamp ASCII string
	// append string to icy
	// compute new sb (size / 16)
	// prepend new sb
	newicy := padIcyString(fmt.Sprintf("Time=%d;%s", unixMicro(time.Now()), string(icy)))
	sb := len([]byte(newicy)) / 16
	fmt.Printf("modified icy metadata={%s} new sb=%d size=%d realsize=%d\n", newicy, sb, sb*16, len([]byte(newicy)))
	return append([]byte{byte(sb)}, newicy...)
}

// LogIcyMeta will connect to given stream and dump icy metadata to the console every time it changes
func (i *Icy) LogIcyMeta(stream io.ReadCloser) error {
	defer func() {
		_ = stream.Close()
	}()
	reader := bufio.NewReader(stream)
	start := time.Now()
	fmt.Printf("Started reading icy metadata from '%s' at %s\n", i.url, start)
	var (
		out       *os.File
		bytesRead int64
	)

	if i.opts.Recording != "" {
		var err error
		out, err = os.Create(i.opts.Recording)
		if err != nil {
			return fmt.Errorf("failed to create output file '%s': %s", i.opts.Recording, err)
		}
		defer func() {
			_ = out.Close()
		}()
	}

	mp3buf := make([]byte, i.icyMetaInt)
	noAds := time.NewTimer(i.opts.NoAdTimeout)
	noIcy := time.NewTimer(i.opts.NoIcyTimeout)

	for {
		select {
		case <-noAds.C:
			if i.numAds == 0 {
				fmt.Printf("no ads found in '%s' - giving up\n", i.url)
				return nil
			}
		case <-noIcy.C:
			if !i.foundIcy {
				fmt.Printf("no icy metadata found in '%s' - giving up\n", i.url)
				return nil
			}
		default:
		}
		if i.numAds > i.opts.MaxAdDetections {
			fmt.Printf("successfully found %d ads in '%s' - stopping\n", i.numAds, i.url)
			return nil
		}

		// skip the first mp3 frame
		c, err := io.ReadFull(reader, mp3buf)
		// If we didn't received icyMetaInt bytes, the stream is ended
		if c != i.icyMetaInt {
			return fmt.Errorf("stream ended prematurely (read only %d out of %d bytes): %v", c, i.icyMetaInt, err)
		}
		if err != nil {
			fmt.Printf("Error reading next %d bytes from '%s'\n", len(mp3buf), i.url)
			return err
		}

		// get the size byte, that is the metadata length in bytes / 16
		sb, err := reader.ReadByte()
		if err != nil {
			fmt.Printf("Error reading next 1 byte from '%s'\n", i.url)
			return err
		}

		if i.opts.Debug {
			fmt.Printf("read %d (mp3+sb) bytes\n", len(mp3buf)+1)
		}

		if i.opts.Recording != "" {
			// write to the recording
			if _, err := out.Write(mp3buf); err != nil {
				return fmt.Errorf("failed writing to file '%s'", i.opts.Recording)
			}
			if !i.opts.DontRecordIcy {
				if i.opts.AddTimestamps { //revive:disable-line:empty-block
					// don't write the sb here - it will change after we append the timing metadata
				} else if _, err := out.Write([]byte{sb}); err != nil {
					return fmt.Errorf("failed writing to file '%s'", i.opts.Recording)
				}
			}
			if i.opts.Debug {
				fmt.Printf("wrote %d (mp3+sb) bytes to output file\n", len(mp3buf)+1*bool2int(!i.opts.DontRecordIcy))
			}
		}
		bytesRead += int64(len(mp3buf) + 1)

		metaSize := int(sb * 16)
		if metaSize == 0 {
			if i.opts.Debug {
				fmt.Println("frame contains zero-len icy-metadata")
			}
			if !i.opts.AddTimestamps {
				continue
			}
		}
		// read the next metaSize bytes it will contain metadata
		// write to the recording
		shouldReturn, returnValue := i.readMetaSizeAndWrite(metaSize, reader, bytesRead, start, sb, out)
		if shouldReturn {
			return returnValue
		}
	}
}

func (i *Icy) readMetaSizeAndWrite(metaSize int, reader *bufio.Reader, bytesRead int64,
	start time.Time, sb byte, out *os.File,
) (bool, error) {
	icy := make([]byte, metaSize)

	n, err := io.ReadFull(reader, icy)
	if err != nil {
		return true, err
	}
	if i.opts.Debug {
		fmt.Printf("read %d icy-metadata bytes\n", n)
	}
	bytesRead += int64(len(icy) + 1)
	var filePercent float64
	if i.isFile && i.fileSize > 0 {
		filePercent = (float64(bytesRead) / float64(i.fileSize)) * 100.0
	}
	i.logIcy(icy, start, int(sb), filePercent)

	if i.opts.Recording != "" && !i.opts.DontRecordIcy {
		if i.opts.AddTimestamps {
			icy = appendTimestamp(icy)
		}

		if _, err := out.Write(icy); err != nil {
			return true, fmt.Errorf("failed writing to file '%s'", i.opts.Recording)
		}
		if i.opts.Debug {
			fmt.Printf("wrote %d (icy) bytes to output file\n", len(icy))
		}
	}
	return false, nil
}

func initOptions() *Options {
	opts := new(Options)
	if err := config.Resolve(opts); err != nil {
		fmt.Printf("Error parsing options: %s\n", err)
		os.Exit(1)
	}
	if opts.URL == "" && opts.URLList == "" && opts.StationID == "" && opts.PartnerID == "" {
		fmt.Printf("No URL specified: -url or -urllist is required (or alternatively -station-id and -partner-id)\n")
		config.Help("icymetadata", opts)
	}
	if opts.IcyMetaInt == 0 {
		opts.IcyMetaInt = defaultIcyMetaInt
	}
	if opts.MaxAdDetections == 0 {
		opts.MaxAdDetections = defaultMaxAdDetections
	}
	if opts.NoAdTimeout == 0 {
		opts.NoAdTimeout = defaultNoAdTimeout
	}
	if opts.NoIcyTimeout == 0 {
		opts.NoIcyTimeout = defaultNoIcyTimeout
	}
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = defaultHTTPTimeout
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = 50
	}
	opts.matchTerms = strings.Split(opts.MatchTerms, ",")
	for i := range opts.matchTerms {
		opts.matchTerms[i] = strings.ToLower(opts.matchTerms[i])
	}
	return opts
}

func urlFromTuneRequest(partnerID, stationID string, tune TuneAPI, opts *Options) (string, error) {
	// make a Tune request
	body, err := tune.Tune(partnerID, stationID, opts)
	if err != nil {
		return "", err
	}

	var objmap map[string]json.RawMessage
	err = json.Unmarshal(body, &objmap)
	if err != nil {
		return "", err
	}

	var bodyfield []json.RawMessage
	err = json.Unmarshal(objmap["body"], &bodyfield)
	if err != nil {
		return "", err
	}

	for _, obj := range bodyfield {
		var bodyobj map[string]any
		err = json.Unmarshal(obj, &bodyobj)
		if err != nil {
			return "", err
		}
		if element, ok := bodyobj["element"]; ok {
			if element.(string) != "audio" {
				continue
			}

			url := bodyobj["url"].(string)
			if opts.Debug {
				fmt.Printf("URL from Tune=%s\n", url)
			}
			// find the audio entry, this has our playback URL
			return url, nil
		}
	}
	if opts.Debug {
		fmt.Printf("TUNERESPONSE=%s\n", string(body))
	}

	return "", errors.New("URL not found in tune response")
}

// findPLSField will return the value of the named field in a PLS playlist
func findPLSField(key, playlist string) string {
	reader := strings.NewReader(playlist)
	sc := bufio.NewScanner(reader)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, key) && strings.Contains(line, "=") {
			_, value, found := strings.Cut(line, "=")
			if found {
				return value
			}
		}
	}
	return ""
}

// PLSFetcher allows mocking of the request to the pls service
type PLSFetcher interface {
	// Fetch retrieves the PLS playlist from the given URL
	Fetch(url string, opts *Options) ([]byte, error)
}

// PLSGetterHTTP will fetch the PLS playlist URL via HTTP request
type PLSGetterHTTP struct{}

// Fetch retrieves the PLS playlist from the given URL
func (p *PLSGetterHTTP) Fetch(url string, opts *Options) ([]byte, error) {
	// first fetch the PLS playlist
	res, err := httpGetRequest(url, nil, opts.HTTPTimeout, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received error response from PLS playlist: %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func fetchPLSAndParseURL(url string, getPLS PLSFetcher, opts *Options) (string, error) {
	body, err := getPLS.Fetch(url, opts)
	if err != nil {
		return "", err
	}

	if opts.Debug {
		fmt.Printf("PLS response=%s\n", string(body))
	}

	var result string
	// File2 is our live stream URL
	if strings.Contains(string(body), "Title2=Live Stream") {
		result = findPLSField("File2", string(body))
	} else {
		// otherwise assume File1 is our live stream URL
		result = findPLSField("File1", string(body))
	}
	if result == "" {
		return "", fmt.Errorf("unable to find Live Stream URL in PLS playlist: %s", url)
	}
	return result, nil
}

func appendAdParams(url string, tune TuneAPI, getPLS PLSFetcher, opts *Options) (string, error) {
	if opts.PartnerID == "" || opts.StationID == "" {
		fmt.Printf("Note: missing -partner-id or -stationd-id, not appending ad params.\n") //nolint:misspell
		return url, nil
	}

	// make tune request
	tuneURL, err := urlFromTuneRequest(opts.PartnerID, opts.StationID, tune, opts)
	if err != nil {
		return "", err
	}
	if strings.Contains(tuneURL, "pls.tunein.com") {
		if opts.Debug {
			fmt.Printf("Tune URL is a PLS playlist -- fetching and parsing\n")
		}
		// we received a PLS preroll URL, need to fetch the PLS playlist and extract stream URL
		urlFromPLS, err := fetchPLSAndParseURL(tuneURL, getPLS, opts)
		if err != nil {
			return "", err
		}
		if opts.Debug {
			fmt.Printf("Parsed URL from PLS playlist: %s\n", urlFromPLS)
		}
		return urlFromPLS, nil
	}
	return tuneURL, nil
}

func httpGetRequest(url string, headers map[string]string, timeout time.Duration, opts *Options) (*http.Response, error) {
	if opts.Debug {
		fmt.Printf("connecting to stream URL: %s\n", url)
	}
	client := &http.Client{Timeout: timeout}
	// nolint:noctx
	req, err := http.NewRequest("GET", url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("error creating http request to '%s': %s", url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending http request to '%s': %s", url, err)
	}
	return resp, nil
}

func streamForURL(url string, opts *Options) (io.ReadCloser, int, error) {
	headers := map[string]string{"Icy-MetaData": "1"}
	resp, err := httpGetRequest(url, headers, 0, opts) // must not set a timeout for the stream connection
	if err != nil {
		return nil, 0, err
	}

	// We sent "Icy-MetaData", we should have a "icy-metaint" in return
	icyMetaIntStr := resp.Header.Get("icy-metaint")
	icyMetaInt := defaultIcyMetaInt
	if icyMetaIntStr == "" && opts.IcyMetaInt == 0 {
		fmt.Println("no icy-metaint header, using default")
	} else if icyMetaIntStr != "" {
		// "icy-metaint" is how often (in bytes) should we receive the meta
		icyMetaInt, err = strconv.Atoi(icyMetaIntStr)
		if err != nil {
			return nil, 0, fmt.Errorf("error parsing icy-metaint '%s' for '%s': %s", icyMetaIntStr, url, err)
		}
		if icyMetaInt > 0 {
			fmt.Printf("parsed icy-metaint header=%d\n", icyMetaInt)
		}
	}
	return resp.Body, icyMetaInt, nil
}

func streamForLocalFile(url string, opts *Options) (io.ReadCloser, int64, error) {
	// assume a local file
	stats, err := os.Stat(url)
	if err != nil {
		fmt.Printf("Failed to get file stats for '%s': %s\n", url, err)
		return nil, 0, err
	}
	fileSize := stats.Size()
	file, err := os.Open(path.Clean(url))
	if err != nil {
		fmt.Printf("Failed opening file '%s': %s\n", url, err)
		return nil, 0, err
	}
	if opts.Debug {
		fmt.Printf("opened local file %s w size=%d\n", url, fileSize)
	}
	return file, fileSize, nil
}

func runStation(station Station, opts *Options) {
	var (
		urlWithParams string
		stream        io.ReadCloser
		fileSize      int64
		icyMetaInt    int
		isFile        bool
		err           error
	)
	if strings.HasPrefix(station.url, "http:") || strings.HasPrefix(station.url, "https:") ||
		(opts.StationID != "" && opts.PartnerID != "") {
		urlWithParams, err = appendAdParams(station.url, new(TuneServer), new(PLSGetterHTTP), opts)
		if urlWithParams != station.url {
			// if the url was modified, update the station
			station.url = urlWithParams
		}
		if err == nil {
			stream, icyMetaInt, err = streamForURL(station.url, opts) //nolint:ineffassign,staticcheck
		}
	} else {
		stream, fileSize, err = streamForLocalFile(station.url, opts)
		icyMetaInt = opts.IcyMetaInt
		isFile = true
	}
	if err != nil {
		fmt.Printf("ERROR: %s\n", err)
		return
	}

	defer func() { _ = stream.Close() }()
	icy := &Icy{
		Station:    station,
		isFile:     isFile,
		fileSize:   fileSize,
		opts:       opts,
		icyMetaInt: icyMetaInt,
	}
	err = icy.LogIcyMeta(stream)
	typ := "stream"
	if isFile {
		typ = "file"
	}
	if err != nil {
		fmt.Printf("Error while connected to %s '%s': %s\n", typ, station.url, err)
	}
}

// parseStationList parses a csv file, where each line contains a stream URL (optionally with other fields)
func parseStationList(file io.Reader) []Station {
	fileScanner := bufio.NewScanner(file)
	fileScanner.Split(bufio.ScanLines)
	var stations []Station
	for fileScanner.Scan() {
		line := fileScanner.Text()
		fields := strings.Split(line, ",")
		var url string
		for _, f := range fields {
			if strings.HasPrefix(f, "http:") || strings.HasPrefix(f, "https:") {
				url = f
			}
		}
		if url != "" {
			stations = append(stations, Station{url: url, csvEntry: line})
		}
	}
	return stations
}

// TuneAPI allows mocking of the Tune API request
type TuneAPI interface {
	// Tune makes a Tune API request to platform and returns the response body
	Tune(stationID, partnerID string, opts *Options) ([]byte, error)
}

// TuneServer will hit Platform's Tune API backend
type TuneServer struct{}

// Tune makes a Tune API request to platform and returns the response body
func (t *TuneServer) Tune(stationID, partnerID string, opts *Options) ([]byte, error) {
	tuneAPI := tuneURITemplate
	if opts.Stage {
		tuneAPI = tuneURITemplateStage
	}

	res, err := httpGetRequest(fmt.Sprintf(tuneAPI, stationID, partnerID), nil, opts.HTTPTimeout, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received error response from Tune API: %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func main() {
	var (
		inputs []Station
		wg     sync.WaitGroup
	)
	opts := initOptions()
	if opts.URLList != "" {
		listFile, err := os.Open(opts.URLList)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		inputs = parseStationList(listFile)
		_ = listFile.Close()
	} else {
		inputs = []Station{{url: opts.URL}}
	}

	p, err := ants.NewPool(opts.Parallelism)
	if err != nil {
		fmt.Printf("Failed creating goroutine pool: %s\n", err)
		os.Exit(1)
	}
	defer p.Release()

	for _, station := range inputs {
		wg.Add(1)
		_ = p.Submit(func() {
			runStation(station, opts)
			wg.Done()
		})
		time.Sleep(time.Second)
	}
	done := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		done <- struct{}{}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGQUIT)
	select {
	case <-sig:
		signal.Stop(sig)
	case <-done:
	}
}
