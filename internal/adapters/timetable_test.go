package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const saintLazareStop = "stop_point:IDFM:monomodalStopPlace:58566"
const cergyLeHautStop = "stop_point:IDFM:monomodalStopPlace:43135"

func patternLeg(t *testing.T, line, board, alight, network, code string) domain.PatternLeg {
	t.Helper()
	leg, err := domain.NewPatternLeg(domain.ProviderLineID(line), domain.ProviderStopID(board), domain.ProviderStopID(alight), domain.NewTransitLabel(network, code))
	if err != nil {
		t.Fatalf("NewPatternLeg() error = %v", err)
	}
	return leg
}

func routePattern(t *testing.T, legs ...domain.PatternLeg) domain.RoutePattern {
	t.Helper()
	pattern, err := domain.NewRoutePattern(legs)
	if err != nil {
		t.Fatalf("NewRoutePattern() error = %v", err)
	}
	return pattern
}

func scheduleServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if !strings.Contains(request.URL.Path, "/route_schedules") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

func cell(dateTime, journey string) string {
	return `{"date_time":"` + dateTime + `","links":[{"id":"` + journey + `","type":"vehicle_journey"}]}`
}

func row(id, name string, cells ...string) string {
	return `{"stop_point":{"id":"` + id + `","name":"` + name + `"},"date_times":[` + strings.Join(cells, ",") + `]}`
}

func schedule(rows ...string) string {
	return `{"table":{"rows":[` + strings.Join(rows, ",") + `]}}`
}

func schedulesPayload(schedules ...string) string {
	return `{"route_schedules":[` + strings.Join(schedules, ",") + `]}`
}

func TestTimetableSnapshotPairsDepartureAndArrivalOfTheSameTrain(t *testing.T) {
	payload := schedulesPayload(schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-1"), cell("20260812T182100", "vj-2")),
		row("stop_point:other", "Bécon les Bruyères", cell("20260812T181600", "vj-1"), cell("20260812T183100", "vj-2")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-1"), cell("20260812T184800", "vj-2")),
	))
	server := scheduleServer(t, payload)
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if got, want := snapshot.LegCount(), 1; got != want {
		t.Fatalf("LegCount() = %d, want %d", got, want)
	}
	for _, observation := range []domain.ObservationID{"vj-1", "vj-2"} {
		if !snapshot.Contains(observation) {
			t.Errorf("snapshot is missing the train %q", observation)
		}
	}
}

func TestTimetableSnapshotRejectsTheRouteRunningInTheOppositeDirection(t *testing.T) {
	// The branch actually serving Cergy-le-Haut announces itself as
	// "Nanterre Université", so only the stop order can be trusted.
	opposite := schedule(
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T180000", "vj-back")),
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T183000", "vj-back")),
	)
	correct := schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-forward")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-forward")),
	)
	server := scheduleServer(t, schedulesPayload(opposite, correct))
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if snapshot.Contains("vj-back") {
		t.Error("a train running towards the origin was accepted; only the stop order distinguishes the direction")
	}
	if !snapshot.Contains("vj-forward") {
		t.Error("the train running in the requested direction was rejected")
	}
}

func TestTimetableSnapshotNeverPairsCellsOfTwoDifferentTrains(t *testing.T) {
	payload := schedulesPayload(schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-1")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-2")),
	))
	server := scheduleServer(t, payload)
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	_, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err == nil {
		t.Error("a departure and an arrival belonging to two different trains were paired into one connection")
	}
}

func TestTimetableSnapshotSkipsABranchThatDoesNotServeTheDestination(t *testing.T) {
	versailles := schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180700", "vj-versailles")),
		row("stop_point:versailles", "Versailles Rive Droite", cell("20260812T183500", "vj-versailles")),
	)
	cergy := schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-cergy")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-cergy")),
	)
	server := scheduleServer(t, schedulesPayload(versailles, cergy))
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if snapshot.Contains("vj-versailles") {
		t.Error("a branch that never reaches the destination was accepted")
	}
	if !snapshot.Contains("vj-cergy") {
		t.Error("the branch serving the destination was rejected")
	}
}

func TestTimetableSnapshotCostsOneCallPerPatternLeg(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		calls++
		_, _ = writer.Write([]byte(schedulesPayload(schedule(
			row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-"+request.URL.Path)),
			row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-"+request.URL.Path)),
		))))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	if _, err := client.TimetableSnapshot(context.Background(),
		routePattern(t,
			patternLeg(t, "line:IDFM:C01373", saintLazareStop, cergyLeHautStop, "Métro", "3"),
			patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L"),
		),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour); err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if got, want := calls, 2; got != want {
		t.Errorf("provider calls = %d, want %d (one timetable grid per leg, not one per candidate train)", got, want)
	}
}

func TestTimetableSnapshotFailsWhenOnlyTheReturnDirectionIsAvailable(t *testing.T) {
	onlyReturn := schedulesPayload(schedule(
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T180000", "vj-back")),
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T183000", "vj-back")),
	))
	server := scheduleServer(t, onlyReturn)
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	_, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err == nil {
		t.Error("a ride was built from a train travelling towards the origin")
	}
}

func TestTimetableSnapshotIgnoresCellsWithoutATrainIdentifier(t *testing.T) {
	payload := schedulesPayload(schedule(
		row(saintLazareStop, "Gare Saint-Lazare", `{"date_time":"20260812T180600","links":[]}`, cell("20260812T182100", "vj-2")),
		row(cergyLeHautStop, "Cergy-le-Haut", `{"date_time":"20260812T183300","links":[]}`, cell("20260812T184800", "vj-2")),
	))
	server := scheduleServer(t, payload)
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01740", saintLazareStop, cergyLeHautStop, "Train Transilien", "L")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if !snapshot.Contains("vj-2") {
		t.Error("the identifiable train was dropped")
	}
	if snapshot.Contains("") {
		t.Error("a connection was built from cells that name no train, so nothing proves they belong together")
	}
}

func TestTimetableSnapshotKeepsTheTrainsOfEveryParallelRoute(t *testing.T) {
	first := schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-route-a")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-route-a")),
	)
	second := schedule(
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T181200", "vj-route-b")),
		row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183900", "vj-route-b")),
	)
	server := scheduleServer(t, schedulesPayload(first, second))
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01742", saintLazareStop, cergyLeHautStop, "RER", "A")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v", err)
	}
	if !snapshot.Contains("vj-route-a") {
		t.Error("the first route was dropped")
	}
	if !snapshot.Contains("vj-route-b") {
		t.Error("a second route serving the same stops was dropped, so half the departures never reach the optimiser")
	}
}

func TestTimetableSnapshotReportsAnEmptyGridAsMissingCoverageNotABrokenContract(t *testing.T) {
	empty := schedulesPayload(schedule(
		row(saintLazareStop, "Gare Saint-Lazare"),
		row(cergyLeHautStop, "Cergy-le-Haut"),
	))
	server := scheduleServer(t, empty)
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	_, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01373", saintLazareStop, cergyLeHautStop, "Métro", "3")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if errors.Is(err, app.ErrProviderUnsupportedContract) {
		t.Error("a well-formed grid holding no departure was reported as a broken provider contract")
	}
	if !errors.Is(err, app.ErrNoTimetableCoverage) {
		t.Fatalf("TimetableSnapshot() error = %v, want ErrNoTimetableCoverage", err)
	}
}

// largeSchedulePayload builds a grid bigger than the journey-sized 1 MiB cap,
// which is the size a real Métro line reaches within one hour.
func largeSchedulePayload(t *testing.T) string {
	t.Helper()
	const filler = "stop_point:IDFM:padding-000000000000000000000000000000000000000000000000"
	rows := []string{
		row(saintLazareStop, "Gare Saint-Lazare", cell("20260812T180600", "vj-1")),
	}
	for len(strings.Join(rows, ",")) < (1 << 20) {
		rows = append(rows, row(filler+strconv.Itoa(len(rows)), "Padding", cell("20260812T181000", "vj-1")))
	}
	rows = append(rows, row(cergyLeHautStop, "Cergy-le-Haut", cell("20260812T183300", "vj-1")))
	payload := schedulesPayload(schedule(rows...))
	if len(payload) <= (1 << 20) {
		t.Fatalf("fixture is %d bytes, want more than 1 MiB", len(payload))
	}
	return payload
}

func TestTimetableSnapshotReadsGridsLargerThanAJourneyResponse(t *testing.T) {
	server := scheduleServer(t, largeSchedulePayload(t))
	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())

	snapshot, err := client.TimetableSnapshot(context.Background(),
		routePattern(t, patternLeg(t, "line:IDFM:C01373", saintLazareStop, cergyLeHautStop, "Métro", "3")),
		time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatalf("TimetableSnapshot() error = %v; a grid over 1 MiB was truncated into invalid JSON", err)
	}
	if !snapshot.Contains("vj-1") {
		t.Error("the train from a large grid was dropped")
	}
}

func TestReplayTransportRecordsABodyLargerThanAJourneyResponse(t *testing.T) {
	directory := t.TempDir()
	body := strings.Repeat("x", (1<<20)+4096)
	stub := &recordingStubTransport{status: http.StatusOK, body: body}
	transport := NewReplayTransport(directory, stub)
	const url = "https://prim.example.test/lines/x/route_schedules"

	if _, err := transport.RoundTrip(newRecordedRequest(t, url)); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	replayed, err := transport.RoundTrip(newRecordedRequest(t, url))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	replayedBody, err := io.ReadAll(replayed.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(replayedBody) != len(body) {
		t.Errorf("replayed %d bytes, want %d; a truncated fixture poisons every later replay", len(replayedBody), len(body))
	}
}
