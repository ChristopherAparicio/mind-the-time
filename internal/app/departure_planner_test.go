package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mind-the-time/internal/domain"
)

type timetableStub struct {
	calls        int
	askedFrom    time.Time
	askedWindow  time.Duration
	snapshot     domain.TimetableSnapshot
	failWith     error
	askedPattern domain.RoutePattern
}

func (s *timetableStub) TimetableSnapshot(_ context.Context, pattern domain.RoutePattern, from time.Time, window time.Duration) (domain.TimetableSnapshot, error) {
	s.calls++
	s.askedPattern = pattern
	s.askedFrom = from
	s.askedWindow = window
	if s.failWith != nil {
		return domain.TimetableSnapshot{}, s.failWith
	}
	return s.snapshot, nil
}

func planTestTime(t *testing.T, clock string) time.Time {
	t.Helper()
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", clock, err)
	}
	return time.Date(2026, time.August, 12, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
}

func twoLegSnapshot(t *testing.T) domain.TimetableSnapshot {
	t.Helper()
	build := func(id, network, code, board, alight, departure, arrival string) domain.ObservedConnection {
		boardID, err := domain.NewStationID(board)
		if err != nil {
			t.Fatalf("NewStationID() error = %v", err)
		}
		alightID, err := domain.NewStationID(alight)
		if err != nil {
			t.Fatalf("NewStationID() error = %v", err)
		}
		connection, err := domain.NewObservedConnection(domain.ObservationID(id), domain.NewTransitLabel(network, code),
			boardID, alightID, board, alight, planTestTime(t, departure), planTestTime(t, arrival))
		if err != nil {
			t.Fatalf("NewObservedConnection() error = %v", err)
		}
		return connection
	}
	snapshot, err := domain.NewTimetableSnapshot([][]domain.ObservedConnection{
		{build("m-1", "Métro", "3", "villiers", "saint-lazare", "18:13", "18:30")},
		{build("l-1", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:37", "19:03")},
	})
	if err != nil {
		t.Fatalf("NewTimetableSnapshot() error = %v", err)
	}
	return snapshot
}

func planRequest(t *testing.T) PlanRequest {
	t.Helper()
	access, err := domain.NewTransferBuffer(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	interchange, err := domain.NewTransferBuffer(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	leg, err := domain.NewPatternLeg("line:IDFM:C01740", "stop_point:a", "stop_point:b", domain.NewTransitLabel("Train Transilien", "L"))
	if err != nil {
		t.Fatalf("NewPatternLeg() error = %v", err)
	}
	pattern, err := domain.NewRoutePattern([]domain.PatternLeg{leg})
	if err != nil {
		t.Fatalf("NewRoutePattern() error = %v", err)
	}
	return PlanRequest{
		Pattern:  pattern,
		Timings:  domain.NewPersonalTimings(access, interchange),
		Earliest: planTestTime(t, "18:00"),
		Window:   30 * time.Minute,
	}
}

func TestDeparturePlannerReturnsTheOptionComputedFromTheTravellersOwnTimes(t *testing.T) {
	timetables := &timetableStub{snapshot: twoLegSnapshot(t)}
	planner := NewDeparturePlanner(timetables, &limiterStub{allow: true})

	options, err := planner.Plan(context.Background(), planRequest(t))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	if got, want := options[0].LeaveAt().Format("15:04"), "18:08"; got != want {
		t.Errorf("LeaveAt() = %s, want %s (boarding at 18:13 minus the traveller's 5 minutes)", got, want)
	}
	if got, want := options[0].Arrival().Format("15:04"), "19:03"; got != want {
		t.Errorf("Arrival() = %s, want %s", got, want)
	}
}

func TestDeparturePlannerReadsTimetablesBeyondTheDepartureWindow(t *testing.T) {
	timetables := &timetableStub{snapshot: twoLegSnapshot(t)}
	planner := NewDeparturePlanner(timetables, &limiterStub{allow: true})
	request := planRequest(t)

	if _, err := planner.Plan(context.Background(), request); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if timetables.askedWindow <= request.Window {
		t.Errorf("timetable horizon = %v, want more than the %v departure window; a train leaving at the end of the window still has to arrive",
			timetables.askedWindow, request.Window)
	}
	if !timetables.askedFrom.Equal(request.Earliest) {
		t.Errorf("timetable read from %v, want %v", timetables.askedFrom, request.Earliest)
	}
}

func TestDeparturePlannerRefusesToSpendACallWhenRateLimited(t *testing.T) {
	timetables := &timetableStub{snapshot: twoLegSnapshot(t)}
	planner := NewDeparturePlanner(timetables, &limiterStub{allow: false})

	_, err := planner.Plan(context.Background(), planRequest(t))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Plan() error = %v, want ErrRateLimited", err)
	}
	if timetables.calls != 0 {
		t.Errorf("timetable calls = %d, want 0 when the rate limiter denies", timetables.calls)
	}
}

func TestDeparturePlannerPropagatesAProviderFailureInsteadOfReturningNoOption(t *testing.T) {
	failure := errors.New("provider down")
	planner := NewDeparturePlanner(&timetableStub{failWith: failure}, &limiterStub{allow: true})

	options, err := planner.Plan(context.Background(), planRequest(t))
	if !errors.Is(err, failure) {
		t.Errorf("Plan() error = %v, want the provider failure; an empty list would look like a real absence", err)
	}
	if options != nil {
		t.Errorf("options = %v, want nil on failure", options)
	}
}
