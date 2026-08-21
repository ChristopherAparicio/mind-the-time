package adapters

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingStubTransport struct {
	calls  int
	status int
	body   string
}

func (t *recordingStubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: t.status,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    request,
	}, nil
}

func newRecordedRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("apiKey", "a-secret-api-key")
	return request
}

func TestReplayTransportServesRecordedResponseWithoutCallingTheProviderAgain(t *testing.T) {
	directory := t.TempDir()
	stub := &recordingStubTransport{status: http.StatusOK, body: `{"journeys":[]}`}
	transport := NewReplayTransport(directory, stub)
	const url = "https://prim.example.test/journeys?from=a&to=b"

	first, err := transport.RoundTrip(newRecordedRequest(t, url))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	firstBody, _ := io.ReadAll(first.Body)
	if got, want := string(firstBody), `{"journeys":[]}`; got != want {
		t.Errorf("first body = %q, want %q", got, want)
	}
	if stub.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", stub.calls)
	}

	second, err := transport.RoundTrip(newRecordedRequest(t, url))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	secondBody, _ := io.ReadAll(second.Body)
	if got, want := string(secondBody), string(firstBody); got != want {
		t.Errorf("replayed body = %q, want %q", got, want)
	}
	if got, want := second.StatusCode, http.StatusOK; got != want {
		t.Errorf("replayed status = %d, want %d", got, want)
	}
	if stub.calls != 1 {
		t.Errorf("provider calls = %d after replay, want 1; the fixture was not used", stub.calls)
	}
}

func TestStrictReplayTransportRejectsFixtureMissWithoutCallingFallback(t *testing.T) {
	directory := t.TempDir()
	stub := &recordingStubTransport{status: http.StatusOK, body: `{"journeys":[]}`}
	transport := NewStrictReplayTransport(directory, stub)

	_, err := transport.RoundTrip(newRecordedRequest(t, "https://prim.example.test/journeys?from=missing"))
	if err == nil {
		t.Fatal("RoundTrip() error = nil, want an error for a missing fixture")
	}
	if got, want := stub.calls, 0; got != want {
		t.Errorf("fallback calls = %d, want %d; strict replay must fail closed", got, want)
	}
}

func TestReplayTransportNeverWritesTheAPIKeyToDisk(t *testing.T) {
	directory := t.TempDir()
	stub := &recordingStubTransport{status: http.StatusOK, body: `{"journeys":[]}`}
	transport := NewReplayTransport(directory, stub)

	if _, err := transport.RoundTrip(newRecordedRequest(t, "https://prim.example.test/journeys?from=a")); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixture was recorded")
	}
	for _, entry := range entries {
		content := decompressedFixture(t, filepath.Join(directory, entry.Name()))
		if bytes.Contains(content, []byte("a-secret-api-key")) {
			t.Errorf("fixture %s leaked the API key", entry.Name())
		}
	}
}

// Fixtures are gzipped, so scanning the raw bytes would pass even on a leak.
func decompressedFixture(t *testing.T, path string) []byte {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("fixture %s is not gzipped: %v", path, err)
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return content
}

func TestReplayTransportKeepsDistinctFixturesPerRequest(t *testing.T) {
	directory := t.TempDir()
	stub := &recordingStubTransport{status: http.StatusOK, body: `{"journeys":[]}`}
	transport := NewReplayTransport(directory, stub)

	for _, url := range []string{
		"https://prim.example.test/journeys?from=a&to=b",
		"https://prim.example.test/journeys?from=a&to=c",
	} {
		if _, err := transport.RoundTrip(newRecordedRequest(t, url)); err != nil {
			t.Fatalf("RoundTrip(%s) error = %v", url, err)
		}
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if got, want := len(entries), 2; got != want {
		t.Errorf("recorded fixtures = %d, want %d; distinct requests must not share a fixture", got, want)
	}
	if stub.calls != 2 {
		t.Errorf("provider calls = %d, want 2", stub.calls)
	}
}

func TestReplayTransportNeverRecordsATimetableHoldingNoDeparture(t *testing.T) {
	directory := t.TempDir()
	empty := `{"route_schedules":[{"table":{"rows":[` +
		`{"stop_point":{"id":"stop_point:IDFM:100001","name":"Villiers"},"date_times":[]},` +
		`{"stop_point":{"id":"stop_point:IDFM:463246","name":"Opéra"},"date_times":[]}]}}]}`
	stub := &recordingStubTransport{status: http.StatusOK, body: empty}
	transport := NewReplayTransport(directory, stub)
	const url = "https://prim.example.test/lines/line:IDFM:C01373/route_schedules?from_datetime=20260810T120000"

	if _, err := transport.RoundTrip(newRecordedRequest(t, url)); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("fixtures written = %d, want 0; a grid without a single departure would starve the optimiser for ever", len(entries))
	}
}

func TestReplayTransportRecordsATimetableThatHoldsDepartures(t *testing.T) {
	directory := t.TempDir()
	populated := `{"route_schedules":[{"table":{"rows":[` +
		`{"stop_point":{"id":"stop_point:IDFM:100001","name":"Villiers"},"date_times":[` + cell("20260812T180600", "vj-1") + `]},` +
		`{"stop_point":{"id":"stop_point:IDFM:463246","name":"Opéra"},"date_times":[` + cell("20260812T181900", "vj-1") + `]}]}}]}`
	stub := &recordingStubTransport{status: http.StatusOK, body: populated}
	transport := NewReplayTransport(directory, stub)
	const url = "https://prim.example.test/lines/line:IDFM:C01373/route_schedules?from_datetime=20260812T180000"

	if _, err := transport.RoundTrip(newRecordedRequest(t, url)); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("fixtures written = %d, want 1; a usable grid must still be recorded", len(entries))
	}
}

func TestReplayTransportNeverRecordsAProviderFailure(t *testing.T) {
	directory := t.TempDir()
	stub := &recordingStubTransport{status: http.StatusTooManyRequests, body: `{"message":"API rate limit exceeded"}`}
	transport := NewReplayTransport(directory, stub)
	const url = "https://prim.example.test/journeys?from=a&to=b"

	if _, err := transport.RoundTrip(newRecordedRequest(t, url)); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	replayed, err := transport.RoundTrip(newRecordedRequest(t, url))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if got, want := replayed.StatusCode, http.StatusTooManyRequests; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	if stub.calls != 2 {
		t.Errorf("provider calls = %d, want 2; a recorded rate limit would replay that failure for ever", stub.calls)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("fixtures written = %d, want 0 for a failed response", len(entries))
	}
}
