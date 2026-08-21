package domain

import (
	"fmt"
	"testing"
	"time"
)

func planTime(t *testing.T, clock string) time.Time {
	t.Helper()
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", clock, err)
	}
	return time.Date(2026, time.August, 12, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
}

func connection(t *testing.T, id, line, code, board, alight, departure, arrival string) ObservedConnection {
	t.Helper()
	boardID, err := NewStationID(board)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", board, err)
	}
	alightID, err := NewStationID(alight)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", alight, err)
	}
	built, err := NewObservedConnection(ObservationID(id), NewTransitLabel(line, code),
		boardID, alightID, board, alight, planTime(t, departure), planTime(t, arrival))
	if err != nil {
		t.Fatalf("NewObservedConnection(%q) error = %v", id, err)
	}
	return built
}

func snapshot(t *testing.T, legs ...[]ObservedConnection) TimetableSnapshot {
	t.Helper()
	built, err := NewTimetableSnapshot(legs)
	if err != nil {
		t.Fatalf("NewTimetableSnapshot() error = %v", err)
	}
	return built
}

func timings(t *testing.T, accessMinutes, interchangeMinutes int) PersonalTimings {
	t.Helper()
	access, err := NewTransferBuffer(time.Duration(accessMinutes) * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer(access) error = %v", err)
	}
	interchange, err := NewTransferBuffer(time.Duration(interchangeMinutes) * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer(interchange) error = %v", err)
	}
	return NewPersonalTimings(access, interchange)
}

func wholeEvening(t *testing.T) DepartureWindow {
	t.Helper()
	window, err := NewDepartureWindow(planTime(t, "17:00"), 3*time.Hour)
	if err != nil {
		t.Fatalf("NewDepartureWindow() error = %v", err)
	}
	return window
}

func describe(options []DepartureOption) []string {
	described := make([]string, 0, len(options))
	for _, option := range options {
		described = append(described, fmt.Sprintf("leave %s arrive %s (%d min)",
			option.LeaveAt().Format("15:04"), option.Arrival().Format("15:04"), int(option.Duration().Minutes())))
	}
	return described
}

func TestPlanDepartureOptionsKeepsBothTheFastestAndTheLatestDeparture(t *testing.T) {
	metro := []ObservedConnection{
		connection(t, "m3-early", "Métro", "3", "villiers", "saint-lazare", "17:59", "18:15"),
		connection(t, "m3-late", "Métro", "3", "villiers", "saint-lazare", "18:13", "18:30"),
	}
	transilien := []ObservedConnection{
		connection(t, "l-early", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:21", "18:47"),
		connection(t, "l-late", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:37", "19:03"),
	}

	options := PlanDepartureOptions(snapshot(t, metro, transilien), timings(t, 5, 5), wholeEvening(t))

	got := describe(options)
	want := []string{
		"leave 17:54 arrive 18:47 (53 min)",
		"leave 18:08 arrive 19:03 (55 min)",
	}
	if len(got) != len(want) {
		t.Fatalf("options = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("options[%d] = %q, want %q (leaving later must survive even when it is slower)", index, got[index], want[index])
		}
	}
}

func TestPlanDepartureOptionsAcceptsAnInterchangeAtTheExactBoundaryAndRejectsOneSecondLess(t *testing.T) {
	for _, test := range []struct {
		name        string
		metroAraval string
		wantOptions int
	}{
		{name: "exactly the declared interchange", metroAraval: "18:16", wantOptions: 1},
		{name: "one minute short", metroAraval: "18:17", wantOptions: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			metro := []ObservedConnection{connection(t, "m", "Métro", "3", "villiers", "saint-lazare", "18:00", test.metroAraval)}
			transilien := []ObservedConnection{connection(t, "l", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:21", "18:47")}

			options := PlanDepartureOptions(snapshot(t, metro, transilien), timings(t, 5, 5), wholeEvening(t))
			if len(options) != test.wantOptions {
				t.Errorf("options = %v, want %d option(s)", describe(options), test.wantOptions)
			}
		})
	}
}

func TestPlanDepartureOptionsRejectsALaterFirstTrainThatBreaksTheChainFurtherDown(t *testing.T) {
	first := []ObservedConnection{
		connection(t, "a-early", "Métro", "3", "villiers", "opera", "18:00", "18:10"),
		connection(t, "a-late", "Métro", "3", "villiers", "opera", "18:20", "18:30"),
	}
	second := []ObservedConnection{
		connection(t, "b-early", "Métro", "13", "opera", "saint-lazare", "18:15", "18:20"),
		connection(t, "b-late", "Métro", "13", "opera", "saint-lazare", "18:40", "18:45"),
	}
	third := []ObservedConnection{
		connection(t, "c", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:25", "18:55"),
	}

	options := PlanDepartureOptions(snapshot(t, first, second, third), timings(t, 0, 5), wholeEvening(t))

	if len(options) != 1 {
		t.Fatalf("options = %v, want exactly the chain that still catches the last train", describe(options))
	}
	var used []string
	for _, connection := range options[0].Connections() {
		used = append(used, string(connection.Observation()))
	}
	want := []string{"a-early", "b-early", "c"}
	for index := range want {
		if used[index] != want[index] {
			t.Fatalf("chain = %v, want %v; a greedy or non-recursive plan would keep the later first train", used, want)
		}
	}
}

func TestPlanDepartureOptionsOnlyEverReturnsConnectionsFromTheSnapshot(t *testing.T) {
	metro := []ObservedConnection{
		connection(t, "m1", "Métro", "3", "villiers", "saint-lazare", "17:59", "18:15"),
		connection(t, "m2", "Métro", "3", "villiers", "saint-lazare", "18:13", "18:30"),
	}
	transilien := []ObservedConnection{
		connection(t, "l1", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:21", "18:47"),
		connection(t, "l2", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:37", "19:03"),
	}
	taken := snapshot(t, metro, transilien)

	for _, option := range PlanDepartureOptions(taken, timings(t, 5, 5), wholeEvening(t)) {
		for _, used := range option.Connections() {
			if !taken.Contains(used.Observation()) {
				t.Errorf("option used observation %q which the provider never returned", used.Observation())
			}
		}
	}
}

func TestPlanDepartureOptionsHonoursTheDepartureWindow(t *testing.T) {
	metro := []ObservedConnection{
		connection(t, "m-early", "Métro", "3", "villiers", "saint-lazare", "17:00", "17:15"),
		connection(t, "m-inside", "Métro", "3", "villiers", "saint-lazare", "18:13", "18:30"),
	}
	transilien := []ObservedConnection{
		connection(t, "l-early", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "17:25", "17:55"),
		connection(t, "l-inside", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:37", "19:03"),
	}
	window, err := NewDepartureWindow(planTime(t, "18:00"), 30*time.Minute)
	if err != nil {
		t.Fatalf("NewDepartureWindow() error = %v", err)
	}

	options := PlanDepartureOptions(snapshot(t, metro, transilien), timings(t, 5, 5), window)

	if len(options) != 1 {
		t.Fatalf("options = %v, want only the chain leaving inside the window", describe(options))
	}
	if got, want := options[0].LeaveAt().Format("15:04"), "18:08"; got != want {
		t.Errorf("LeaveAt() = %s, want %s", got, want)
	}
}

// exhaustivePlan is a deliberately naive reference: it enumerates every chain,
// keeps the feasible ones and reduces them. Any divergence from the planner is
// a planner bug.
func exhaustivePlan(snapshotLegs [][]ObservedConnection, personal PersonalTimings, window DepartureWindow) []DepartureOption {
	var chains [][]ObservedConnection
	var walk func(leg int, current []ObservedConnection)
	walk = func(leg int, current []ObservedConnection) {
		if leg == len(snapshotLegs) {
			chains = append(chains, append([]ObservedConnection(nil), current...))
			return
		}
		for _, candidate := range snapshotLegs[leg] {
			if leg > 0 && !personal.allowsInterchange(current[len(current)-1].arrival, candidate.departure) {
				continue
			}
			walk(leg+1, append(current, candidate))
		}
	}
	walk(0, nil)

	var options []DepartureOption
	for _, chain := range chains {
		leaveAt := personal.leaveAtFor(chain[0].departure)
		if !window.contains(leaveAt) {
			continue
		}
		options = append(options, DepartureOption{leaveAt: leaveAt, connections: chain})
	}
	return nonDominatedOptions(options)
}

func TestPlanDepartureOptionsMatchesAnExhaustiveReferenceOnGeneratedTimetables(t *testing.T) {
	for seed := 0; seed < 60; seed++ {
		legCount := 2 + seed%3
		legs := make([][]ObservedConnection, 0, legCount)
		for leg := 0; leg < legCount; leg++ {
			trains := 2 + (seed+leg)%4
			connections := make([]ObservedConnection, 0, trains)
			for train := 0; train < trains; train++ {
				depart := 8*60 + leg*17 + train*13 + seed%11
				ride := 6 + (seed+leg+train)%14
				connections = append(connections, connection(t,
					fmt.Sprintf("s%d-l%d-t%d", seed, leg, train), "Métro", fmt.Sprintf("%d", leg+1),
					fmt.Sprintf("stop-%d", leg), fmt.Sprintf("stop-%d", leg+1),
					fmt.Sprintf("%02d:%02d", depart/60, depart%60),
					fmt.Sprintf("%02d:%02d", (depart+ride)/60, (depart+ride)%60)))
			}
			legs = append(legs, connections)
		}
		personal := timings(t, seed%7, seed%9)
		window, err := NewDepartureWindow(planTime(t, "07:00"), 6*time.Hour)
		if err != nil {
			t.Fatalf("NewDepartureWindow() error = %v", err)
		}

		got := describe(PlanDepartureOptions(snapshot(t, legs...), personal, window))
		want := describe(exhaustivePlan(legs, personal, window))
		if len(got) != len(want) {
			t.Fatalf("seed %d: planner = %v, exhaustive reference = %v", seed, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("seed %d: planner[%d] = %q, reference = %q", seed, index, got[index], want[index])
			}
		}
	}
}

func TestPlanDepartureOptionsPicksTheEarliestArrivalNotTheFirstCompatibleTrain(t *testing.T) {
	first := []ObservedConnection{
		connection(t, "m", "Métro", "3", "villiers", "saint-lazare", "18:00", "18:10"),
	}
	second := []ObservedConnection{
		connection(t, "slow-but-earlier", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:15", "19:00"),
		connection(t, "later-but-faster", "Train Transilien", "L", "saint-lazare", "cergy-le-haut", "18:20", "18:40"),
	}

	options := PlanDepartureOptions(snapshot(t, first, second), timings(t, 0, 0), wholeEvening(t))

	if len(options) != 1 {
		t.Fatalf("options = %v, want 1", describe(options))
	}
	if got, want := options[0].Arrival().Format("15:04"), "18:40"; got != want {
		t.Errorf("Arrival() = %s, want %s; the first compatible train was kept instead of the one arriving earliest", got, want)
	}
}

func transitLegWithIdentity(t *testing.T, line, board, alight, network, code, departure, arrival string) ItineraryLeg {
	t.Helper()
	from, err := NewStationID("stop-" + code)
	if err != nil {
		t.Fatalf("NewStationID() error = %v", err)
	}
	to, err := NewStationID("stop-" + code + "-end")
	if err != nil {
		t.Fatalf("NewStationID() error = %v", err)
	}
	leg, err := NewTransitLegWithProviderIdentity(from, to, "From", "To",
		planTime(t, departure), planTime(t, arrival), NewTransitLabel(network, code),
		ProviderLineID(line), ProviderStopID(board), ProviderStopID(alight))
	if err != nil {
		t.Fatalf("NewTransitLegWithProviderIdentity() error = %v", err)
	}
	return leg
}

func TestItineraryRoutePatternUsesProviderIdentifiersNotDisplayLabels(t *testing.T) {
	walk, err := NewWalkingLeg(planTime(t, "18:00"), planTime(t, "18:05"))
	if err != nil {
		t.Fatalf("NewWalkingLeg() error = %v", err)
	}
	itinerary, err := NewItinerary([]ItineraryLeg{
		walk,
		transitLegWithIdentity(t, "line:IDFM:C01373", "stop_point:villiers", "stop_point:saint-lazare", "Métro", "3", "18:05", "18:25"),
		transitLegWithIdentity(t, "line:IDFM:C01740", "stop_point:saint-lazare", "stop_point:cergy", "Train Transilien", "L", "18:30", "19:00"),
	})
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}

	pattern, err := itinerary.RoutePattern()
	if err != nil {
		t.Fatalf("RoutePattern() error = %v", err)
	}
	legs := pattern.Legs()
	if got, want := len(legs), 2; got != want {
		t.Fatalf("pattern legs = %d, want %d (walking is not part of the pattern)", got, want)
	}
	if got, want := legs[1].Line(), ProviderLineID("line:IDFM:C01740"); got != want {
		t.Errorf("legs[1].Line() = %q, want %q", got, want)
	}
	if got, want := legs[1].BoardAt(), ProviderStopID("stop_point:saint-lazare"); got != want {
		t.Errorf("legs[1].BoardAt() = %q, want %q", got, want)
	}
	if got, want := pattern.Combination(), "Métro 3 + Train Transilien L"; got != want {
		t.Errorf("Combination() = %q, want %q", got, want)
	}
}

func TestItineraryRoutePatternRefusesALegWithoutProviderIdentifiers(t *testing.T) {
	from, _ := NewStationID("villiers")
	to, _ := NewStationID("saint-lazare")
	unidentified, err := NewTransitLegWithLabel(from, to, planTime(t, "18:05"), planTime(t, "18:25"), NewTransitLabel("Métro", "3"))
	if err != nil {
		t.Fatalf("NewTransitLegWithLabel() error = %v", err)
	}
	itinerary, err := NewItinerary([]ItineraryLeg{unidentified})
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}

	if _, err := itinerary.RoutePattern(); err == nil {
		t.Error("a pattern was derived from display labels alone, which cannot distinguish two branches of the same line")
	}
}

func TestItineraryRoutePatternNeverSilentlyDropsAnUnidentifiedLeg(t *testing.T) {
	from, _ := NewStationID("saint-lazare")
	to, _ := NewStationID("cergy-le-haut")
	unidentified, err := NewTransitLegWithLabel(from, to, planTime(t, "18:30"), planTime(t, "19:00"), NewTransitLabel("Train Transilien", "L"))
	if err != nil {
		t.Fatalf("NewTransitLegWithLabel() error = %v", err)
	}
	itinerary, err := NewItinerary([]ItineraryLeg{
		transitLegWithIdentity(t, "line:IDFM:C01373", "stop_point:villiers", "stop_point:saint-lazare", "Métro", "3", "18:05", "18:25"),
		unidentified,
	})
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}

	pattern, err := itinerary.RoutePattern()
	if err == nil {
		t.Fatalf("RoutePattern() returned %q for a two-leg journey whose second leg has no identifiers; the ride would be planned as if it did not exist", pattern.Combination())
	}
}
