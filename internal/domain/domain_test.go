package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewPlaceNormalizesAndValidatesText(t *testing.T) {
	place, err := NewPlace("  Café Rivoli  ")
	if err != nil {
		t.Fatalf("NewPlace() error = %v", err)
	}
	if got, want := place.Text(), "Café Rivoli"; got != want {
		t.Errorf("Place.Text() = %q, want %q", got, want)
	}

	_, err = NewPlace(" \t\n ")
	if !errors.Is(err, ErrInvalidPlace) {
		t.Errorf("NewPlace(blank) error = %v, want ErrInvalidPlace", err)
	}

	_, err = NewPlace(strings.Repeat("a", 257))
	if !errors.Is(err, ErrInvalidPlace) {
		t.Errorf("NewPlace(257 characters) error = %v, want ErrInvalidPlace", err)
	}

	_, err = NewPlace(strings.Repeat("é", 256))
	if err != nil {
		t.Errorf("NewPlace(256 characters) error = %v, want nil", err)
	}
}

func TestWeeklyScheduleRequiresValidActiveDays(t *testing.T) {
	departureTime, err := NewDepartureTime("08:00")
	if err != nil {
		t.Fatalf("NewDepartureTime() error = %v", err)
	}
	schedule, err := NewWeeklySchedule([]time.Weekday{time.Monday, time.Wednesday}, departureTime)
	if err != nil {
		t.Fatalf("NewWeeklySchedule() error = %v", err)
	}
	if !schedule.IsActive(time.Monday) || schedule.IsActive(time.Tuesday) {
		t.Error("WeeklySchedule did not retain its active days")
	}

	_, err = NewWeeklySchedule(nil, departureTime)
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Errorf("NewWeeklySchedule(nil) error = %v, want ErrInvalidSchedule", err)
	}
}

func TestDepartureTimeValidatesHHMM(t *testing.T) {
	departureTime, err := NewDepartureTime("08:05")
	if err != nil {
		t.Fatalf("NewDepartureTime() error = %v", err)
	}
	if got, want := departureTime.String(), "08:05"; got != want {
		t.Errorf("DepartureTime.String() = %q, want %q", got, want)
	}

	for _, invalid := range []string{"8:05", "08:5", "24:00", "08:60", "08-05"} {
		_, err := NewDepartureTime(invalid)
		if !errors.Is(err, ErrInvalidDepartureTime) {
			t.Errorf("NewDepartureTime(%q) error = %v, want ErrInvalidDepartureTime", invalid, err)
		}
	}
}

func TestWeeklyScheduleConstructsParisDepartureInstant(t *testing.T) {
	departureTime, err := NewDepartureTime("08:15")
	if err != nil {
		t.Fatalf("NewDepartureTime() error = %v", err)
	}
	schedule, err := NewWeeklySchedule([]time.Weekday{time.Monday}, departureTime)
	if err != nil {
		t.Fatalf("NewWeeklySchedule() error = %v", err)
	}

	departure, err := schedule.DepartureAt(time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("WeeklySchedule.DepartureAt() error = %v", err)
	}
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("time.LoadLocation(Europe/Paris) error = %v", err)
	}
	want := time.Date(2026, time.August, 3, 8, 15, 0, 0, paris)
	if !departure.Equal(want) || departure.Location().String() != "Europe/Paris" {
		t.Errorf("WeeklySchedule.DepartureAt() = %v in %s, want %v in Europe/Paris", departure, departure.Location(), want)
	}
}

func TestTransferBufferRequiresPositiveDuration(t *testing.T) {
	buffer, err := NewTransferBuffer(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	if got, want := buffer.Duration(), 5*time.Minute; got != want {
		t.Errorf("TransferBuffer.Duration() = %v, want %v", got, want)
	}

	_, err = NewTransferBuffer(-time.Minute)
	if !errors.Is(err, ErrInvalidTransferBuffer) {
		t.Errorf("NewTransferBuffer(-1m) error = %v, want ErrInvalidTransferBuffer", err)
	}
}

func TestCommuteExposesItsBufferValues(t *testing.T) {
	commute := testCommute(t)
	if got, want := commute.MaximumInitialWalk().Duration(), 5*time.Minute; got != want {
		t.Errorf("Commute.MaximumInitialWalk() = %v, want %v", got, want)
	}
	if got, want := commute.SaintLazareMinimumTransferTime().Duration(), 5*time.Minute; got != want {
		t.Errorf("Commute.SaintLazareMinimumTransferTime() = %v, want %v", got, want)
	}
}

func TestSelectingAParcoursKeepsItinerariesContainingAnUnlabelledLeg(t *testing.T) {
	start := testTime(8, 0, 0)
	labelled := func(from, to StationID, network, code string, offset, length time.Duration) ItineraryLeg {
		t.Helper()
		leg, err := NewTransitLegWithLabel(from, to, start.Add(offset), start.Add(offset+length), NewTransitLabel(network, code))
		if err != nil {
			t.Fatalf("NewTransitLegWithLabel() error = %v", err)
		}
		return leg
	}
	unlabelled, err := NewTransitLeg("saint-lazare", "pont-cardinet", start.Add(40*time.Minute), start.Add(45*time.Minute))
	if err != nil {
		t.Fatalf("NewTransitLeg() error = %v", err)
	}
	itinerary := testItinerary(t,
		labelled("villiers", "auber", "Métro", "3", 0, 15*time.Minute),
		labelled("auber", "cergy-le-haut", "RER", "A", 20*time.Minute, 15*time.Minute),
		unlabelled,
	)

	if !NewPreferredLines("Métro 3 + RER A").MatchesItinerary(itinerary) {
		t.Error("a leg without a line label vetoed the selected parcours; the provider does not label every section")
	}
	if NewPreferredLines("Métro 9").MatchesItinerary(itinerary) {
		t.Error("an itinerary was accepted for a parcours none of its labelled legs belong to")
	}
}

func TestPersonalWalkNeverDiscardsItineraryAndShiftsDeparture(t *testing.T) {
	commute := testCommute(t)
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t,
		testWalkingLeg(t, start, start.Add(8*time.Minute)),
		testTransitLeg(t, "home-stop", "gare-du-nord", start.Add(8*time.Minute), start.Add(20*time.Minute)),
	)

	if !itinerary.IsEligibleFor(commute) {
		t.Error("an 8 minute provider walk was discarded although the traveller only declared 5 minutes")
	}
	if got, want := itinerary.LeaveAt(commute.MaximumInitialWalk()), start.Add(3*time.Minute); !got.Equal(want) {
		t.Errorf("LeaveAt() = %v, want %v (boarding at 08:08 minus the traveller's 5 minutes)", got, want)
	}
}

func TestLeaveAtFallsBackToItineraryDepartureWithoutTransitLeg(t *testing.T) {
	commute := testCommute(t)
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t, testWalkingLeg(t, start, start.Add(12*time.Minute)))

	if got, want := itinerary.LeaveAt(commute.MaximumInitialWalk()), start; !got.Equal(want) {
		t.Errorf("LeaveAt() = %v, want %v", got, want)
	}
}

func TestTightInterchangeIsReportedInsteadOfDiscarded(t *testing.T) {
	commute := testCommute(t)
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t,
		testTransitLeg(t, "gare-du-nord", SaintLazareStationID, start, start.Add(15*time.Minute)),
		testTransitLeg(t, SaintLazareStationID, "saint-augustin", start.Add(15*time.Minute+299*time.Second), start.Add(30*time.Minute)),
	)

	if !itinerary.IsEligibleFor(commute) {
		t.Error("a tight interchange was discarded, want the itinerary kept and reported as not fitting")
	}
	if itinerary.InterchangeFits(commute.SaintLazareMinimumTransferTime()) {
		t.Error("InterchangeFits() = true for a 4:59 gap against a declared 5:00, want false")
	}
}

func TestInterchangeFitsAtExactBoundary(t *testing.T) {
	commute := testCommute(t)
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t,
		testTransitLeg(t, "gare-du-nord", SaintLazareStationID, start, start.Add(15*time.Minute)),
		testTransitLeg(t, SaintLazareStationID, "saint-augustin", start.Add(20*time.Minute), start.Add(30*time.Minute)),
	)

	if !itinerary.InterchangeFits(commute.SaintLazareMinimumTransferTime()) {
		t.Error("InterchangeFits() = false for an exact 5:00 gap, want true")
	}
}

func TestTightestInterchangeUsesSmallestGapAcrossAllStations(t *testing.T) {
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t,
		testTransitLeg(t, "gare-du-nord", "chatelet", start, start.Add(15*time.Minute)),
		testTransitLeg(t, "chatelet", "opera", start.Add(25*time.Minute), start.Add(30*time.Minute)),
		testTransitLeg(t, "opera", "office-stop", start.Add(30*time.Minute+time.Second), start.Add(40*time.Minute)),
	)

	tightest, hasTransfer := itinerary.TightestInterchange()
	if !hasTransfer {
		t.Fatal("TightestInterchange() reported no transfer, want one")
	}
	if got, want := tightest, time.Second; got != want {
		t.Errorf("TightestInterchange() = %v, want %v (the smallest gap, not the first one)", got, want)
	}
}

func TestTightestInterchangeReportsAbsenceWithSingleTransitLeg(t *testing.T) {
	start := testTime(8, 0, 0)
	itinerary := testItinerary(t, testTransitLeg(t, "origin", "destination", start, start.Add(20*time.Minute)))

	if _, hasTransfer := itinerary.TightestInterchange(); hasTransfer {
		t.Error("TightestInterchange() reported a transfer on a single transit leg, want none")
	}
}

func TestRankCandidatesOrdersLexicographically(t *testing.T) {
	commute := testCommute(t)
	base := testTime(8, 0, 0)

	fewestTransfers := testItinerary(t,
		testTransitLeg(t, "origin", "fewest", base, base.Add(90*time.Minute)),
	)
	shorterDuration := testItinerary(t,
		testTransitLeg(t, "origin", "shorter", base, base.Add(15*time.Minute)),
		testTransitLeg(t, "shorter", "destination", base.Add(20*time.Minute), base.Add(60*time.Minute)),
	)
	shorterTransferGap := testItinerary(t,
		testTransitLeg(t, "origin", "short-gap", base, base.Add(15*time.Minute)),
		testWalkingLeg(t, base.Add(15*time.Minute), base.Add(25*time.Minute)),
		testTransitLeg(t, "short-gap", "destination", base.Add(25*time.Minute), base.Add(70*time.Minute)),
	)
	longerTransferGap := testItinerary(t,
		testTransitLeg(t, "origin", "long-gap", base, base.Add(10*time.Minute)),
		testWalkingLeg(t, base.Add(10*time.Minute), base.Add(35*time.Minute)),
		testTransitLeg(t, "long-gap", "destination", base.Add(35*time.Minute), base.Add(70*time.Minute)),
	)
	largerSaintLazareMargin := testItinerary(t,
		testTransitLeg(t, "origin", SaintLazareStationID, base, base.Add(10*time.Minute)),
		testTransitLeg(t, SaintLazareStationID, "margin-high", base.Add(20*time.Minute), base.Add(25*time.Minute)),
		testTransitLeg(t, "margin-high", "destination", base.Add(30*time.Minute), base.Add(40*time.Minute)),
	)
	smallerSaintLazareMargin := testItinerary(t,
		testTransitLeg(t, "origin", SaintLazareStationID, base, base.Add(10*time.Minute)),
		testTransitLeg(t, SaintLazareStationID, "margin-low", base.Add(15*time.Minute), base.Add(20*time.Minute)),
		testTransitLeg(t, "margin-low", "destination", base.Add(30*time.Minute), base.Add(40*time.Minute)),
	)
	ranked := RankCandidates(commute, []Itinerary{
		longerTransferGap,
		smallerSaintLazareMargin,
		shorterDuration,
		fewestTransfers,
		largerSaintLazareMargin,
		shorterTransferGap,
	})
	if got, want := len(ranked), 6; got != want {
		t.Fatalf("RankCandidates() returned %d candidates, want %d", got, want)
	}

	assertRankedItineraries(t, ranked, []Itinerary{
		largerSaintLazareMargin,
		smallerSaintLazareMargin,
		shorterDuration,
		shorterTransferGap,
		longerTransferGap,
		fewestTransfers,
	})
}

func TestRankCandidatesPreservesInputOrderForExactTies(t *testing.T) {
	commute := testCommute(t)
	base := testTime(8, 0, 0)
	first := testItinerary(t,
		testTransitLeg(t, "origin", "first", base, base.Add(10*time.Minute)),
		testTransitLeg(t, "first", "destination", base.Add(15*time.Minute), base.Add(30*time.Minute)),
	)
	second := testItinerary(t,
		testTransitLeg(t, "origin", "second", base, base.Add(10*time.Minute)),
		testTransitLeg(t, "second", "destination", base.Add(15*time.Minute), base.Add(30*time.Minute)),
	)

	ranked := RankCandidates(commute, []Itinerary{second, first})
	assertRankedItineraries(t, ranked, []Itinerary{second, first})
}

func TestRankCandidatesRanksLargerSafetyMarginFirst(t *testing.T) {
	commute := testCommute(t)
	base := testTime(8, 0, 0)
	largerMargin := testItinerary(t,
		testTransitLeg(t, "origin", "hub", base, base.Add(10*time.Minute)),
		testTransitLeg(t, "hub", "buffered", base.Add(20*time.Minute), base.Add(25*time.Minute)),
		testTransitLeg(t, "buffered", "destination", base.Add(30*time.Minute), base.Add(40*time.Minute)),
	)
	smallerMargin := testItinerary(t,
		testTransitLeg(t, "origin", "hub", base, base.Add(10*time.Minute)),
		testTransitLeg(t, "hub", "ordinary-a", base.Add(15*time.Minute), base.Add(20*time.Minute)),
		testTransitLeg(t, "ordinary-a", "destination", base.Add(30*time.Minute), base.Add(40*time.Minute)),
	)

	ranked := RankCandidates(commute, []Itinerary{smallerMargin, largerMargin})
	assertRankedItineraries(t, ranked, []Itinerary{largerMargin, smallerMargin})
}

func assertRankedItineraries(t *testing.T, got, want []Itinerary) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranked candidates = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].legs[0].to != want[index].legs[0].to {
			t.Errorf("ranked[%d] first transit destination = %q, want %q", index, got[index].legs[0].to, want[index].legs[0].to)
		}
	}
}

func testCommute(t *testing.T) Commute {
	t.Helper()
	origin, err := NewPlace("Café Rivoli")
	if err != nil {
		t.Fatalf("NewPlace(origin) error = %v", err)
	}
	destination, err := NewPlace("Cergy-le-Haut")
	if err != nil {
		t.Fatalf("NewPlace(destination) error = %v", err)
	}
	departureTime, err := NewDepartureTime("08:00")
	if err != nil {
		t.Fatalf("NewDepartureTime() error = %v", err)
	}
	schedule, err := NewWeeklySchedule([]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}, departureTime)
	if err != nil {
		t.Fatalf("NewWeeklySchedule() error = %v", err)
	}
	buffer, err := NewTransferBuffer(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	commute, err := NewCommute(origin, destination, schedule, buffer, buffer, NewPreferredLines(""))
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	return commute
}

func testItinerary(t *testing.T, legs ...ItineraryLeg) Itinerary {
	t.Helper()
	itinerary, err := NewItinerary(legs)
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}
	return itinerary
}

func testWalkingLeg(t *testing.T, departure, arrival time.Time) ItineraryLeg {
	t.Helper()
	leg, err := NewWalkingLeg(departure, arrival)
	if err != nil {
		t.Fatalf("NewWalkingLeg() error = %v", err)
	}
	return leg
}

func testTransitLeg(t *testing.T, from, to string, departure, arrival time.Time) ItineraryLeg {
	t.Helper()
	fromStation, err := NewStationID(from)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", from, err)
	}
	toStation, err := NewStationID(to)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", to, err)
	}
	leg, err := NewTransitLeg(fromStation, toStation, departure, arrival)
	if err != nil {
		t.Fatalf("NewTransitLeg() error = %v", err)
	}
	return leg
}

func testTime(hour, minute, second int) time.Time {
	return time.Date(2026, time.August, 3, hour, minute, second, 0, time.UTC)
}

func TestPreferredLinesFiltering(t *testing.T) {
	baseTime := testTime(18, 0, 0)
	origin, _ := NewPlace("10 Rue de Rivoli, Paris")
	destination, _ := NewPlace("Cergy-le-Haut")
	departureTime, _ := NewDepartureTime("18:00")
	schedule, _ := NewWeeklySchedule([]time.Weekday{time.Monday}, departureTime)
	buffer, _ := NewTransferBuffer(5 * time.Minute)

	from, _ := NewStationID("villiers")
	to, _ := NewStationID("republique")
	transit3, _ := NewTransitLegWithLabel(from, to, baseTime, baseTime.Add(10*time.Minute), NewTransitLabel("Métro", "3"))
	transitA, _ := NewTransitLegWithLabel(from, to, baseTime, baseTime.Add(10*time.Minute), NewTransitLabel("RER", "A"))

	itin3 := testItinerary(t, transit3)
	itinA := testItinerary(t, transitA)

	commute3, _ := NewCommute(origin, destination, schedule, buffer, buffer, NewPreferredLines("3"))
	if !itin3.IsEligibleFor(commute3) {
		t.Errorf("itin3 expected eligible for commute3 (Métro 3)")
	}
	if itinA.IsEligibleFor(commute3) {
		t.Errorf("itinA expected ineligible for commute3 (RER A when preferred is 3)")
	}

	commuteBoth, _ := NewCommute(origin, destination, schedule, buffer, buffer, NewPreferredLines("RER A, Métro 3"))
	if !itin3.IsEligibleFor(commuteBoth) {
		t.Errorf("itin3 expected eligible for commuteBoth")
	}
	if !itinA.IsEligibleFor(commuteBoth) {
		t.Errorf("itinA expected eligible for commuteBoth")
	}
}

func TestPreferredLinesNeverAcceptsALineWhoseCodeMerelyContainsTheRequestedOne(t *testing.T) {
	from, err := NewStationID("villiers")
	if err != nil {
		t.Fatalf("NewStationID() error = %v", err)
	}
	to, err := NewStationID("republique")
	if err != nil {
		t.Fatalf("NewStationID() error = %v", err)
	}
	start := testTime(8, 0, 0)
	metro13, err := NewTransitLegWithLabel(from, to, start, start.Add(10*time.Minute), NewTransitLabel("Métro", "13"))
	if err != nil {
		t.Fatalf("NewTransitLegWithLabel() error = %v", err)
	}

	for _, requested := range []string{"Métro 1", "Métro 3", "1", "3"} {
		if NewPreferredLines(requested).MatchesLeg(metro13) {
			t.Errorf("selecting %q accepted a Métro 13 leg", requested)
		}
	}
	for _, requested := range []string{"Métro 13", "13", "Métro"} {
		if !NewPreferredLines(requested).MatchesLeg(metro13) {
			t.Errorf("selecting %q rejected the Métro 13 leg it names", requested)
		}
	}
}
