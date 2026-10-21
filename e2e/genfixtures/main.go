// Command genfixtures rebuilds e2e/fixtures from a synthetic PRIM upstream.
//
// The e2e server replays provider responses in strict mode: a request whose
// exact URL has no recorded fixture fails. Instead of reproducing those URLs
// by hand, this program drives the same adapter and application code the
// server runs — place resolution, journey discovery sweeps and exclusions,
// timetable reads — against a deterministic in-process PRIM stand-in, and the
// recording replay transport writes one fixture per URL actually requested.
//
// The synthetic network serves the scenario the e2e suites assert: a
// Métro 3 + Transilien L combination (with its Saint-Lazare interchange) and a
// direct RER A alternative between the recorded origin and destination.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mind-the-time/internal/adapters"
	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const (
	defaultEndpoint = "https://prim.iledefrance-mobilites.fr/marketplace/v2/navitia/journeys"
	primTimeLayout  = "20060102T150405"

	originText      = "10 Rue de Rivoli, 75004 Paris"
	destinationText = "Cergy-le-Haut"

	originPlaceID      = "2.352936;48.855416"
	destinationPlaceID = "stop_area:IDFM:63244"

	metroLineID = "line:IDFM:C01373"
	trainLineID = "line:IDFM:C01739"
	rerLineID   = "line:IDFM:C01742"

	metroBoardStopID  = "stop_point:IDFM:22101"
	metroAlightStopID = "stop_point:IDFM:22087"
	trainBoardStopID  = "stop_point:IDFM:41001"
	trainAlightStopID = "stop_point:IDFM:41090"
	rerBoardStopID    = "stop_point:IDFM:71517"
	rerAlightStopID   = "stop_point:IDFM:71090"
)

type primPlace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type primDisplay struct {
	PhysicalMode string `json:"physical_mode"`
	Code         string `json:"code"`
}

type primLink struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type primSection struct {
	Type              string      `json:"type"`
	Mode              string      `json:"mode,omitempty"`
	TransferType      string      `json:"transfer_type,omitempty"`
	DepartureDateTime string      `json:"departure_date_time"`
	ArrivalDateTime   string      `json:"arrival_date_time"`
	From              primPlace   `json:"from"`
	To                primPlace   `json:"to"`
	DisplayDef        primDisplay `json:"display_informations"`
	Links             []primLink  `json:"links,omitempty"`
}

type primJourney struct {
	DepartureDateTime string        `json:"departure_date_time"`
	ArrivalDateTime   string        `json:"arrival_date_time"`
	Duration          int64         `json:"duration"`
	Sections          []primSection `json:"sections"`
}

type primScheduleCell struct {
	DateTime string     `json:"date_time"`
	Links    []primLink `json:"links"`
}

type primScheduleRow struct {
	StopPoint primPlace          `json:"stop_point"`
	DateTimes []primScheduleCell `json:"date_times"`
}

// syntheticPRIM answers the three provider endpoints the adapters call. Every
// response derives deterministically from the request URL, so regenerating the
// fixtures is reproducible.
type syntheticPRIM struct{}

func (syntheticPRIM) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload any
	switch {
	case strings.Contains(request.URL.Path, "/places"):
		payload = placesPayload(request.URL.Query().Get("q"))
	case strings.Contains(request.URL.Path, "/route_schedules"):
		payload = schedulesPayload(request.URL)
	case strings.Contains(request.URL.Path, "/journeys"):
		payload = journeysPayload(request.URL)
	}
	if payload == nil {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"unknown synthetic endpoint"}`)),
			Request:    request,
		}, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

func placesPayload(query string) any {
	var id string
	switch {
	case strings.Contains(query, "Rivoli"):
		id = originPlaceID
	case strings.Contains(query, "Cergy"):
		id = destinationPlaceID
	default:
		return nil
	}
	return map[string]any{"places": []primPlace{{ID: id, Name: query}}}
}

func journeysPayload(target *url.URL) any {
	query := target.Query()
	departure, err := time.ParseInLocation(primTimeLayout, query.Get("datetime"), paris())
	if err != nil {
		return nil
	}
	forbidden := make(map[string]bool)
	for _, lineID := range query["forbidden_uris[]"] {
		forbidden[lineID] = true
	}

	journeys := make([]primJourney, 0, 2)
	if !forbidden[metroLineID] && !forbidden[trainLineID] {
		journeys = append(journeys, metroAndTrainJourney(departure))
	}
	if !forbidden[rerLineID] {
		journeys = append(journeys, rerJourney(departure))
	}
	return map[string]any{"journeys": journeys}
}

// metroAndTrainJourney is the combination both e2e suites look for: Métro 3 to
// Saint-Lazare, then Transilien L to Cergy-le-Haut, with a 9-minute
// interchange that fits the declared 7 minutes.
func metroAndTrainJourney(departure time.Time) primJourney {
	stamp := func(offset time.Duration) string { return departure.Add(offset).Format(primTimeLayout) }
	return primJourney{
		DepartureDateTime: stamp(0),
		ArrivalDateTime:   stamp(58 * time.Minute),
		Duration:          int64((58 * time.Minute).Seconds()),
		Sections: []primSection{
			{
				Type: "street_network", Mode: "walking",
				DepartureDateTime: stamp(0), ArrivalDateTime: stamp(4 * time.Minute),
				From: primPlace{Name: originText},
				To:   primPlace{ID: metroBoardStopID, Name: "Sentier"},
			},
			{
				Type:              "public_transport",
				DepartureDateTime: stamp(6 * time.Minute), ArrivalDateTime: stamp(16 * time.Minute),
				From:       primPlace{ID: metroBoardStopID, Name: "Sentier"},
				To:         primPlace{ID: metroAlightStopID, Name: "Saint-Lazare"},
				DisplayDef: primDisplay{PhysicalMode: "Métro", Code: "3"},
				Links:      []primLink{{ID: metroLineID, Type: "line"}},
			},
			{
				Type: "transfer", TransferType: "walking",
				DepartureDateTime: stamp(16 * time.Minute), ArrivalDateTime: stamp(18 * time.Minute),
				From: primPlace{ID: metroAlightStopID, Name: "Saint-Lazare"},
				To:   primPlace{ID: trainBoardStopID, Name: "Saint-Lazare"},
			},
			{
				Type:              "public_transport",
				DepartureDateTime: stamp(25 * time.Minute), ArrivalDateTime: stamp(55 * time.Minute),
				From:       primPlace{ID: trainBoardStopID, Name: "Saint-Lazare"},
				To:         primPlace{ID: trainAlightStopID, Name: "Cergy-le-Haut"},
				DisplayDef: primDisplay{PhysicalMode: "Transilien", Code: "L"},
				Links:      []primLink{{ID: trainLineID, Type: "line"}},
			},
			{
				Type: "street_network", Mode: "walking",
				DepartureDateTime: stamp(55 * time.Minute), ArrivalDateTime: stamp(58 * time.Minute),
				From: primPlace{ID: trainAlightStopID, Name: "Cergy-le-Haut"},
				To:   primPlace{Name: destinationText},
			},
		},
	}
}

func rerJourney(departure time.Time) primJourney {
	stamp := func(offset time.Duration) string { return departure.Add(offset).Format(primTimeLayout) }
	return primJourney{
		DepartureDateTime: stamp(0),
		ArrivalDateTime:   stamp(53 * time.Minute),
		Duration:          int64((53 * time.Minute).Seconds()),
		Sections: []primSection{
			{
				Type: "street_network", Mode: "walking",
				DepartureDateTime: stamp(0), ArrivalDateTime: stamp(5 * time.Minute),
				From: primPlace{Name: originText},
				To:   primPlace{ID: rerBoardStopID, Name: "Châtelet-Les Halles"},
			},
			{
				Type:              "public_transport",
				DepartureDateTime: stamp(8 * time.Minute), ArrivalDateTime: stamp(50 * time.Minute),
				From:       primPlace{ID: rerBoardStopID, Name: "Châtelet-Les Halles"},
				To:         primPlace{ID: rerAlightStopID, Name: "Cergy-le-Haut"},
				DisplayDef: primDisplay{PhysicalMode: "RER", Code: "A"},
				Links:      []primLink{{ID: rerLineID, Type: "line"}},
			},
			{
				Type: "street_network", Mode: "walking",
				DepartureDateTime: stamp(50 * time.Minute), ArrivalDateTime: stamp(53 * time.Minute),
				From: primPlace{ID: rerAlightStopID, Name: "Cergy-le-Haut"},
				To:   primPlace{Name: destinationText},
			},
		},
	}
}

func schedulesPayload(target *url.URL) any {
	lineID := lineFromSchedulePath(target.Path)
	from, err := time.ParseInLocation(primTimeLayout, target.Query().Get("from_datetime"), paris())
	if err != nil {
		return nil
	}

	var rows []primScheduleRow
	switch lineID {
	case metroLineID:
		rows = scheduleRows(
			primPlace{ID: metroBoardStopID, Name: "Sentier"},
			primPlace{ID: metroAlightStopID, Name: "Saint-Lazare"},
			"M3", from, 2*time.Minute, 5*time.Minute, 10*time.Minute, 12,
		)
	case trainLineID:
		rows = scheduleRows(
			primPlace{ID: trainBoardStopID, Name: "Saint-Lazare"},
			primPlace{ID: trainAlightStopID, Name: "Cergy-le-Haut"},
			"L", from, 15*time.Minute, 15*time.Minute, 40*time.Minute, 10,
		)
	case rerLineID:
		rows = scheduleRows(
			primPlace{ID: rerBoardStopID, Name: "Châtelet-Les Halles"},
			primPlace{ID: rerAlightStopID, Name: "Cergy-le-Haut"},
			"A", from, 8*time.Minute, 10*time.Minute, 42*time.Minute, 12,
		)
	default:
		return nil
	}
	return map[string]any{
		"route_schedules": []map[string]any{{"table": map[string]any{"rows": rows}}},
	}
}

func lineFromSchedulePath(path string) string {
	_, tail, found := strings.Cut(path, "/lines/")
	if !found {
		return ""
	}
	line, _, found := strings.Cut(tail, "/route_schedules")
	if !found {
		return ""
	}
	return line
}

// scheduleRows builds the two-row grid the timetable adapter pairs: one
// departure row and one arrival row sharing vehicle-journey identifiers.
func scheduleRows(
	board, alight primPlace,
	runPrefix string,
	from time.Time,
	firstDeparture, headway, ride time.Duration,
	count int,
) []primScheduleRow {
	boardCells := make([]primScheduleCell, 0, count)
	alightCells := make([]primScheduleCell, 0, count)
	for run := 0; run < count; run++ {
		departure := from.Add(firstDeparture + time.Duration(run)*headway)
		journey := primLink{ID: fmt.Sprintf("vehicle_journey:%s:%02d", runPrefix, run), Type: "vehicle_journey"}
		boardCells = append(boardCells, primScheduleCell{DateTime: departure.Format(primTimeLayout), Links: []primLink{journey}})
		alightCells = append(alightCells, primScheduleCell{DateTime: departure.Add(ride).Format(primTimeLayout), Links: []primLink{journey}})
	}
	return []primScheduleRow{
		{StopPoint: board, DateTimes: boardCells},
		{StopPoint: alight, DateTimes: alightCells},
	}
}

func paris() *time.Location {
	location, err := domain.NetworkLocation()
	if err != nil {
		log.Fatalf("load network timezone: %v", err)
	}
	return location
}

type nopCache struct{}

func (nopCache) Get(string) ([]domain.Itinerary, bool) { return nil, false }
func (nopCache) Set(string, []domain.Itinerary)        {}

type allowLimiter struct{}

func (allowLimiter) Allow() bool { return true }

func main() {
	output := flag.String("out", "e2e/fixtures", "fixture directory to (re)build")
	endpoint := flag.String("endpoint", defaultEndpoint, "journeys endpoint the e2e server is configured with (IDFM_PRIM_ROUTE_URL)")
	flag.Parse()

	if err := os.RemoveAll(*output); err != nil {
		log.Fatalf("clear fixture directory: %v", err)
	}

	transport := adapters.NewReplayTransport(*output, syntheticPRIM{})
	client := &http.Client{Transport: transport}
	provider := adapters.NewPRIMClient(*endpoint, "e2e-replay-only-key", client)
	routes := app.NewRouteService(provider, nopCache{}, allowLimiter{})

	commute := e2eCommute()
	ctx := context.Background()

	// Mirror the exploration the e2e suites run: 2026-08-11, 18:00 to 18:20.
	serviceDate := time.Date(2026, time.August, 11, 0, 0, 0, 0, paris())
	start := mustDepartureTime("18:00")
	end := mustDepartureTime("18:20")
	instants, err := app.ExplorationInstants(serviceDate, start, end, paris())
	if err != nil {
		log.Fatalf("exploration instants: %v", err)
	}
	candidates, err := app.NewRoutePatternDiscovery(routes).Discover(ctx, commute, instants)
	if err != nil {
		log.Fatalf("discovery: %v", err)
	}
	expected := false
	for _, candidate := range candidates {
		combination := candidate.Pattern.Combination()
		if strings.Contains(combination, "Métro 3") && strings.Contains(combination, "Transilien L") {
			expected = true
		}
		log.Printf("discovered candidate: %s", combination)
	}
	if !expected {
		log.Fatal("the Métro 3 + Transilien L candidate the e2e suites assert was not discovered")
	}

	// Mirror the planning step: 2026-08-12, 18:00 to 18:30, per saved pattern.
	planner := app.NewDeparturePlanner(provider, allowLimiter{})
	windowStart := time.Date(2026, time.August, 12, 18, 0, 0, 0, paris())
	for _, candidate := range candidates {
		options, err := planner.Plan(ctx, app.PlanRequestForPattern(candidate.Pattern, commute, windowStart, 30*time.Minute))
		if err != nil {
			log.Fatalf("plan %s: %v", candidate.Pattern.Combination(), err)
		}
		if len(options) == 0 {
			log.Fatalf("plan %s: no viable departure option", candidate.Pattern.Combination())
		}
		log.Printf("planned %s: %d departure options", candidate.Pattern.Combination(), len(options))
	}

	fixtures, err := filepath.Glob(filepath.Join(*output, "*.fixture.gz"))
	if err != nil {
		log.Fatalf("list fixtures: %v", err)
	}
	log.Printf("recorded %d fixtures in %s", len(fixtures), *output)
}

func e2eCommute() domain.Commute {
	origin, err := domain.NewPlace(originText)
	if err != nil {
		log.Fatalf("origin: %v", err)
	}
	destination, err := domain.NewPlace(destinationText)
	if err != nil {
		log.Fatalf("destination: %v", err)
	}
	schedule, err := domain.NewWeeklySchedule(
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		mustDepartureTime("18:00"),
	)
	if err != nil {
		log.Fatalf("schedule: %v", err)
	}
	walk, err := domain.NewTransferBuffer(5 * time.Minute)
	if err != nil {
		log.Fatalf("walk buffer: %v", err)
	}
	transfer, err := domain.NewTransferBuffer(7 * time.Minute)
	if err != nil {
		log.Fatalf("transfer buffer: %v", err)
	}
	commute, err := domain.NewCommute(origin, destination, schedule, walk, transfer, domain.NewPreferredLines(""))
	if err != nil {
		log.Fatalf("commute: %v", err)
	}
	return commute
}

func mustDepartureTime(value string) domain.DepartureTime {
	departure, err := domain.NewDepartureTime(value)
	if err != nil {
		log.Fatalf("departure time %q: %v", value, err)
	}
	return departure
}
