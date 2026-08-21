package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mind-the-time/internal/domain"
)

func TestRoutePatternDiscoveryEvaluatesEveryRepresentativeInstantAndKeepsDistinctProviderPatterns(t *testing.T) {
	_, commute, err := NewCommute(validCommuteInput())
	if err != nil {
		t.Fatal(err)
	}
	first := savedPatternItinerary(t, "line:branch-a")
	second := savedPatternItinerary(t, "line:branch-b")
	firstInstant := time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)
	secondInstant := firstInstant.Add(24 * time.Hour)
	routes := &routeEvaluatorStub{responses: [][]domain.Itinerary{{first}, {second, first}}}

	candidates, err := NewRoutePatternDiscovery(routes).Discover(context.Background(), commute, []time.Time{firstInstant, secondInstant})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got, want := len(routes.requests), 2; got != want {
		t.Fatalf("route evaluations = %d, want %d", got, want)
	}
	if !routes.requests[0].Departure.Equal(firstInstant) || !routes.requests[1].Departure.Equal(secondInstant) {
		t.Fatalf("representative instants = %#v, want [%v %v]", routes.requests, firstInstant, secondInstant)
	}
	if got, want := len(candidates), 2; got != want {
		t.Fatalf("candidates = %d, want %d; matching labels must not merge distinct provider IDs", got, want)
	}
	if candidates[0].Display != "Train L" || candidates[1].Display != "Train L" {
		t.Fatalf("candidate displays = %#v, want presentation labels", candidates)
	}
	for _, candidate := range candidates {
		leg := candidate.Pattern.Legs()[0]
		if leg.BoardName() != "Origin" || leg.AlightName() != "Destination" {
			t.Fatalf("discovered endpoint names = %q to %q, want Origin to Destination", leg.BoardName(), leg.AlightName())
		}
	}
}

func TestSavedCombinationRuntimePlannerScopesCallsToSelectedIDsAndReturnsDeterministicOrder(t *testing.T) {
	first := savedRoutePattern(t, "line:branch-a")
	second := savedRoutePattern(t, "line:branch-b")
	record := CommuteRecord{
		Commute: mustRuntimeCommute(t),
		SavedCombinations: []SavedRouteCombination{
			{ID: "a", Pattern: first},
			{ID: "b", Pattern: second},
		},
	}
	planner := &patternPlannerStub{}
	results, err := NewSavedCombinationRuntimePlanner(planner).Plan(
		context.Background(), record, []SavedCombinationID{"b", "b", "a"}, time.Now(), time.Hour,
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := len(planner.requests), 2; got != want {
		t.Fatalf("pattern planner calls = %d, want %d for distinct selected IDs only", got, want)
	}
	if !planner.requests[0].Pattern.Equal(second) || !planner.requests[1].Pattern.Equal(first) {
		t.Fatalf("planned patterns = %#v, want selected exact patterns", planner.requests)
	}
	if got, want := len(results), 2; got != want {
		t.Fatalf("results = %d, want %d", got, want)
	}
	if results[0].Combination.ID != "a" || results[1].Combination.ID != "b" {
		t.Fatalf("results = %#v, want deterministic opaque-ID ordering", results)
	}
}

func TestSavedCombinationRuntimePlannerKeepsViableResultsAfterCoverageFailure(t *testing.T) {
	coverage := savedRoutePattern(t, "line:branch-a")
	viable := savedRoutePattern(t, "line:branch-b")
	planner := &patternPlannerStub{responses: map[string]patternPlanResponse{
		coverage.Fingerprint(): {err: ErrNoTimetableCoverage},
		viable.Fingerprint():   {options: []domain.DepartureOption{runtimeDepartureOption(t, "viable", "18:20", "18:45")}},
	}}
	results, err := NewSavedCombinationRuntimePlanner(planner).Plan(
		context.Background(),
		CommuteRecord{Commute: mustRuntimeCommute(t), SavedCombinations: []SavedRouteCombination{{ID: "a", Pattern: coverage}, {ID: "b", Pattern: viable}}},
		[]SavedCombinationID{"a", "b"},
		time.Now(),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := len(results), 2; got != want {
		t.Fatalf("results = %d, want %d", got, want)
	}
	byID := savedCombinationPlansByID(results)
	if got := byID["a"]; got.Outcome != SavedCombinationNoTimetableCoverage || len(got.Options) != 0 {
		t.Fatalf("coverage result = %#v, want explicit coverage outcome without options", got)
	}
	if got := byID["b"]; got.Outcome != SavedCombinationViable || len(got.Options) != 1 {
		t.Fatalf("viable result = %#v, want one usable option", got)
	}
}

func TestSavedCombinationRuntimePlannerRepresentsProviderUnavailableSeparately(t *testing.T) {
	coverage := savedRoutePattern(t, "line:branch-a")
	unavailable := savedRoutePattern(t, "line:branch-b")
	viable := savedRoutePattern(t, "line:branch-c")
	planner := &patternPlannerStub{responses: map[string]patternPlanResponse{
		coverage.Fingerprint():    {err: ErrNoTimetableCoverage},
		unavailable.Fingerprint(): {err: ErrProviderUnavailable},
		viable.Fingerprint():      {options: []domain.DepartureOption{runtimeDepartureOption(t, "viable", "18:20", "18:45")}},
	}}
	results, err := NewSavedCombinationRuntimePlanner(planner).Plan(
		context.Background(),
		CommuteRecord{Commute: mustRuntimeCommute(t), SavedCombinations: []SavedRouteCombination{{ID: "a", Pattern: coverage}, {ID: "b", Pattern: unavailable}, {ID: "c", Pattern: viable}}},
		[]SavedCombinationID{"a", "b", "c"},
		time.Now(),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	byID := savedCombinationPlansByID(results)
	if got := byID["a"].Outcome; got != SavedCombinationNoTimetableCoverage {
		t.Fatalf("coverage outcome = %v, want %v", got, SavedCombinationNoTimetableCoverage)
	}
	if got := byID["b"].Outcome; got != SavedCombinationProviderUnavailable {
		t.Fatalf("provider outcome = %v, want %v", got, SavedCombinationProviderUnavailable)
	}
	if got := byID["c"]; got.Outcome != SavedCombinationViable || len(got.Options) != 1 {
		t.Fatalf("viable result = %#v, want one usable option", got)
	}
}

func TestSavedCombinationRuntimePlannerRanksLatestDepartureBeforeArrivalAndOpaqueID(t *testing.T) {
	patterns := []domain.RoutePattern{
		savedRoutePattern(t, "line:branch-a"),
		savedRoutePattern(t, "line:branch-b"),
		savedRoutePattern(t, "line:branch-c"),
		savedRoutePattern(t, "line:branch-d"),
	}
	combinations := []SavedRouteCombination{
		{ID: "a", Pattern: patterns[0]},
		{ID: "b", Pattern: patterns[1]},
		{ID: "c", Pattern: patterns[2]},
		{ID: "d", Pattern: patterns[3]},
	}
	planner := &patternPlannerStub{responses: map[string]patternPlanResponse{
		patterns[0].Fingerprint(): {options: []domain.DepartureOption{runtimeDepartureOption(t, "early", "18:10", "18:25")}},
		patterns[1].Fingerprint(): {options: []domain.DepartureOption{runtimeDepartureOption(t, "later-long", "18:20", "19:10")}},
		patterns[2].Fingerprint(): {options: []domain.DepartureOption{runtimeDepartureOption(t, "later-earlier-arrival", "18:20", "18:55")}},
		patterns[3].Fingerprint(): {options: []domain.DepartureOption{runtimeDepartureOption(t, "later-tie", "18:20", "18:55")}},
	}}
	results, err := NewSavedCombinationRuntimePlanner(planner).Plan(
		context.Background(), CommuteRecord{Commute: mustRuntimeCommute(t), SavedCombinations: combinations}, []SavedCombinationID{"a", "b", "c", "d"}, time.Now(), time.Hour,
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := savedCombinationPlanIDs(results), []SavedCombinationID{"c", "d", "b", "a"}; !sameSavedCombinationIDs(got, want) {
		t.Fatalf("ranking = %v, want %v", got, want)
	}
}

func TestSavedCombinationRuntimePlannerReturnsUnexpectedAndContextErrors(t *testing.T) {
	pattern := savedRoutePattern(t, "line:branch-a")
	for _, failure := range []error{errors.New("unexpected"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			planner := &patternPlannerStub{err: failure}
			results, err := NewSavedCombinationRuntimePlanner(planner).Plan(
				context.Background(),
				CommuteRecord{Commute: mustRuntimeCommute(t), SavedCombinations: []SavedRouteCombination{{ID: "a", Pattern: pattern}}},
				[]SavedCombinationID{"a"},
				time.Now(),
				time.Hour,
			)
			if !errors.Is(err, failure) {
				t.Fatalf("Plan() error = %v, want %v", err, failure)
			}
			if results != nil {
				t.Fatalf("Plan() results = %#v, want nil after operation error", results)
			}
		})
	}
}

type routeEvaluatorStub struct {
	responses [][]domain.Itinerary
	requests  []RouteRequest
}

func (s *routeEvaluatorStub) Evaluate(_ context.Context, request RouteRequest) ([]domain.Itinerary, error) {
	s.requests = append(s.requests, request)
	return s.responses[len(s.requests)-1], nil
}

type patternPlannerStub struct {
	requests  []PlanRequest
	responses map[string]patternPlanResponse
	err       error
}

type patternPlanResponse struct {
	options []domain.DepartureOption
	err     error
}

func (s *patternPlannerStub) Plan(_ context.Context, request PlanRequest) ([]domain.DepartureOption, error) {
	s.requests = append(s.requests, request)
	if response, ok := s.responses[request.Pattern.Fingerprint()]; ok {
		return append([]domain.DepartureOption(nil), response.options...), response.err
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

func savedPatternItinerary(t *testing.T, line string) domain.Itinerary {
	t.Helper()
	start := time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)
	from, err := domain.NewStationID("origin")
	if err != nil {
		t.Fatal(err)
	}
	to, err := domain.NewStationID("destination")
	if err != nil {
		t.Fatal(err)
	}
	leg, err := domain.NewTransitLegWithProviderIdentity(
		from, to, "Origin", "Destination", start, start.Add(time.Hour), domain.NewTransitLabel("Train", "L"), domain.ProviderLineID(line), "stop:origin", "stop:destination",
	)
	if err != nil {
		t.Fatal(err)
	}
	itinerary, err := domain.NewItinerary([]domain.ItineraryLeg{leg})
	if err != nil {
		t.Fatal(err)
	}
	return itinerary
}

func savedRoutePattern(t *testing.T, line string) domain.RoutePattern {
	t.Helper()
	pattern, err := savedPatternItinerary(t, line).RoutePattern()
	if err != nil {
		t.Fatal(err)
	}
	return pattern
}

func mustRuntimeCommute(t *testing.T) domain.Commute {
	t.Helper()
	_, commute, err := NewCommute(validCommuteInput())
	if err != nil {
		t.Fatal(err)
	}
	return commute
}

func runtimeDepartureOption(t *testing.T, observation, departure, arrival string) domain.DepartureOption {
	t.Helper()
	from, err := domain.NewStationID("origin")
	if err != nil {
		t.Fatal(err)
	}
	to, err := domain.NewStationID("destination")
	if err != nil {
		t.Fatal(err)
	}
	departureTime := runtimePlanTime(t, departure)
	arrivalTime := runtimePlanTime(t, arrival)
	connection, err := domain.NewObservedConnection(domain.ObservationID(observation), domain.NewTransitLabel("Train", "L"), from, to, "Origin", "Destination", departureTime, arrivalTime)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := domain.NewTimetableSnapshot([][]domain.ObservedConnection{{connection}})
	if err != nil {
		t.Fatal(err)
	}
	buffer, err := domain.NewTransferBuffer(0)
	if err != nil {
		t.Fatal(err)
	}
	window, err := domain.NewDepartureWindow(departureTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	options := domain.PlanDepartureOptions(snapshot, domain.NewPersonalTimings(buffer, buffer), window)
	if len(options) != 1 {
		t.Fatalf("PlanDepartureOptions() = %#v, want one option", options)
	}
	return options[0]
}

func runtimePlanTime(t *testing.T, clock string) time.Time {
	t.Helper()
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, time.August, 3, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
}

func savedCombinationPlansByID(plans []SavedCombinationPlan) map[SavedCombinationID]SavedCombinationPlan {
	byID := make(map[SavedCombinationID]SavedCombinationPlan, len(plans))
	for _, plan := range plans {
		byID[plan.Combination.ID] = plan
	}
	return byID
}

func savedCombinationPlanIDs(plans []SavedCombinationPlan) []SavedCombinationID {
	ids := make([]SavedCombinationID, 0, len(plans))
	for _, plan := range plans {
		ids = append(ids, plan.Combination.ID)
	}
	return ids
}

func sameSavedCombinationIDs(got, want []SavedCombinationID) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
