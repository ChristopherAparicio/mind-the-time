package adapters

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const recordedResponseSeparator = "\n"

// Fixtures must hold the largest legitimate response, timetable grids included.
const maxRecordedBodyBytes = maxTimetableBodyBytes

// ReplayTransport serves recorded provider responses from disk and records the
// ones it has never seen. Only the status line and body are stored: request
// headers carry the API key and must never reach the fixture directory.
type ReplayTransport struct {
	directory string
	next      http.RoundTripper
	strict    bool
	mutex     sync.Mutex
}

func NewReplayTransport(directory string, next http.RoundTripper) *ReplayTransport {
	return newReplayTransport(directory, next, false)
}

func NewStrictReplayTransport(directory string, next http.RoundTripper) *ReplayTransport {
	return newReplayTransport(directory, next, true)
}

func newReplayTransport(directory string, next http.RoundTripper, strict bool) *ReplayTransport {
	if next == nil {
		next = http.DefaultTransport
	}
	return &ReplayTransport{directory: directory, next: next, strict: strict}
}

func (t *ReplayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	path := t.fixturePath(request)

	t.mutex.Lock()
	defer t.mutex.Unlock()

	if status, body, err := readRecordedResponse(path); err == nil {
		return newReplayedResponse(request, status, body), nil
	} else if t.strict {
		return nil, fmt.Errorf("replay fixture unavailable for %s: %w", request.URL, err)
	}

	response, err := t.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRecordedBodyBytes))
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	// Recording a 429 or a 500 would replay that failure for ever.
	if isRecordableStatus(response.StatusCode) && !holdsNoDeparture(body) {
		if writeErr := writeRecordedResponse(path, response.StatusCode, body); writeErr != nil {
			return nil, writeErr
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func isRecordableStatus(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

// PRIM answers a past window with 200, every stop and no departure. Recording
// that grid would replay its emptiness for ever, exactly as an error would.
func holdsNoDeparture(body []byte) bool {
	var decoded primRouteSchedules
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.RouteSchedules) == 0 {
		return false
	}
	for _, schedule := range decoded.RouteSchedules {
		for _, row := range schedule.Table.Rows {
			if len(row.DateTimes) > 0 {
				return false
			}
		}
	}
	return true
}

func (t *ReplayTransport) fixturePath(request *http.Request) string {
	digest := sha256.Sum256([]byte(request.Method + " " + request.URL.String()))
	return filepath.Join(t.directory, hex.EncodeToString(digest[:])+".fixture.gz")
}

func readRecordedResponse(path string) (int, []byte, error) {
	compressed, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return 0, nil, fmt.Errorf("unreadable fixture %s: %w", path, err)
	}
	defer func() { _ = reader.Close() }()
	recorded, err := io.ReadAll(io.LimitReader(reader, maxRecordedBodyBytes))
	if err != nil {
		return 0, nil, err
	}
	statusText, body, found := strings.Cut(string(recorded), recordedResponseSeparator)
	if !found {
		return 0, nil, fmt.Errorf("malformed fixture %s", path)
	}
	status, err := strconv.Atoi(statusText)
	if err != nil {
		return 0, nil, fmt.Errorf("malformed status in fixture %s: %w", path, err)
	}
	return status, []byte(body), nil
}

func writeRecordedResponse(path string, status int, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append([]byte(strconv.Itoa(status)+recordedResponseSeparator), body...)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, compressed.Bytes(), 0o600)
}

func newReplayedResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}
