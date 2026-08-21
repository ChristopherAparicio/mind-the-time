package domain

import (
	"errors"
	"sort"
	"time"
)

var ErrInvalidTimetableSnapshot = errors.New("invalid timetable snapshot")

type ObservationID string

// ObservedConnection is one boarding-to-alighting move copied verbatim from a
// single provider observation. Planning may only ever chain these.
type ObservedConnection struct {
	observation ObservationID
	label       TransitLabel
	boardAt     StationID
	boardName   string
	alightAt    StationID
	alightName  string
	departure   time.Time
	arrival     time.Time
}

func NewObservedConnection(
	observation ObservationID,
	label TransitLabel,
	boardAt, alightAt StationID,
	boardName, alightName string,
	departure, arrival time.Time,
) (ObservedConnection, error) {
	if observation == "" || !boardAt.valid() || !alightAt.valid() || !validLegTimes(departure, arrival) {
		return ObservedConnection{}, ErrInvalidTimetableSnapshot
	}
	return ObservedConnection{
		observation: observation,
		label:       label,
		boardAt:     boardAt,
		boardName:   boardName,
		alightAt:    alightAt,
		alightName:  alightName,
		departure:   departure,
		arrival:     arrival,
	}, nil
}

func (c ObservedConnection) Observation() ObservationID { return c.observation }
func (c ObservedConnection) Label() TransitLabel        { return c.label }
func (c ObservedConnection) BoardAt() StationID         { return c.boardAt }
func (c ObservedConnection) AlightAt() StationID        { return c.alightAt }
func (c ObservedConnection) BoardName() string          { return c.boardName }
func (c ObservedConnection) AlightName() string         { return c.alightName }
func (c ObservedConnection) Departure() time.Time       { return c.departure }
func (c ObservedConnection) Arrival() time.Time         { return c.arrival }

// TimetableSnapshot holds, for each leg of the chosen route pattern, every
// connection the provider reported for it.
type TimetableSnapshot struct {
	legs [][]ObservedConnection
}

func NewTimetableSnapshot(legs [][]ObservedConnection) (TimetableSnapshot, error) {
	if len(legs) == 0 {
		return TimetableSnapshot{}, ErrInvalidTimetableSnapshot
	}
	copied := make([][]ObservedConnection, 0, len(legs))
	for _, leg := range legs {
		if len(leg) == 0 {
			return TimetableSnapshot{}, ErrInvalidTimetableSnapshot
		}
		copied = append(copied, append([]ObservedConnection(nil), leg...))
	}
	return TimetableSnapshot{legs: copied}, nil
}

func (s TimetableSnapshot) LegCount() int { return len(s.legs) }

func (s TimetableSnapshot) Contains(observation ObservationID) bool {
	for _, leg := range s.legs {
		for _, connection := range leg {
			if connection.observation == observation {
				return true
			}
		}
	}
	return false
}

// DepartureOption is one feasible chain: when to leave, which connections to
// take, and when it lands.
type DepartureOption struct {
	leaveAt     time.Time
	connections []ObservedConnection
}

func (o DepartureOption) LeaveAt() time.Time { return o.leaveAt }
func (o DepartureOption) Arrival() time.Time { return o.connections[len(o.connections)-1].arrival }
func (o DepartureOption) Duration() time.Duration {
	return o.Arrival().Sub(o.leaveAt)
}

func (o DepartureOption) Connections() []ObservedConnection {
	return append([]ObservedConnection(nil), o.connections...)
}

type DepartureWindow struct {
	earliest time.Time
	latest   time.Time
}

func NewDepartureWindow(earliest time.Time, width time.Duration) (DepartureWindow, error) {
	if earliest.IsZero() || width < 0 {
		return DepartureWindow{}, ErrInvalidTimetableSnapshot
	}
	return DepartureWindow{earliest: earliest, latest: earliest.Add(width)}, nil
}

func (w DepartureWindow) contains(moment time.Time) bool {
	return !moment.Before(w.earliest) && !moment.After(w.latest)
}

// PlanDepartureOptions walks the snapshot backwards: the last connection fixes
// the deadline for the one before it, and so on. Working forwards cannot know
// which train to aim for.
func PlanDepartureOptions(snapshot TimetableSnapshot, timings PersonalTimings, window DepartureWindow) []DepartureOption {
	lastLeg := len(snapshot.legs) - 1
	bestCompletion := make([]map[ObservationID][]ObservedConnection, len(snapshot.legs))

	bestCompletion[lastLeg] = make(map[ObservationID][]ObservedConnection, len(snapshot.legs[lastLeg]))
	for _, connection := range snapshot.legs[lastLeg] {
		bestCompletion[lastLeg][connection.observation] = []ObservedConnection{connection}
	}

	for leg := lastLeg - 1; leg >= 0; leg-- {
		bestCompletion[leg] = make(map[ObservationID][]ObservedConnection, len(snapshot.legs[leg]))
		for _, connection := range snapshot.legs[leg] {
			var best []ObservedConnection
			for _, next := range snapshot.legs[leg+1] {
				completion, reachable := bestCompletion[leg+1][next.observation]
				if !reachable || !timings.allowsInterchange(connection.arrival, next.departure) {
					continue
				}
				if best == nil || completion[len(completion)-1].arrival.Before(best[len(best)-1].arrival) {
					best = completion
				}
			}
			if best != nil {
				bestCompletion[leg][connection.observation] = append([]ObservedConnection{connection}, best...)
			}
		}
	}

	options := make([]DepartureOption, 0, len(snapshot.legs[0]))
	for _, connection := range snapshot.legs[0] {
		chain, reachable := bestCompletion[0][connection.observation]
		if !reachable {
			continue
		}
		leaveAt := timings.leaveAtFor(connection.departure)
		if !window.contains(leaveAt) {
			continue
		}
		options = append(options, DepartureOption{leaveAt: leaveAt, connections: chain})
	}
	return nonDominatedOptions(options)
}

// nonDominatedOptions keeps every option that is not beaten on all three of
// duration, departure lateness and arrival, so "fastest" is never silently
// chosen on the traveller's behalf.
func nonDominatedOptions(options []DepartureOption) []DepartureOption {
	type outcome struct{ leaveAt, arrival time.Time }
	kept := make([]DepartureOption, 0, len(options))
	seen := make(map[outcome]struct{}, len(options))
	for _, candidate := range options {
		dominated := false
		for _, other := range options {
			if dominates(other, candidate) {
				dominated = true
				break
			}
		}
		if dominated {
			continue
		}
		// Chains differing only by an interchangeable middle train are the same offer.
		key := outcome{leaveAt: candidate.leaveAt, arrival: candidate.Arrival()}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, candidate)
	}
	sort.SliceStable(kept, func(left, right int) bool {
		if kept[left].Duration() != kept[right].Duration() {
			return kept[left].Duration() < kept[right].Duration()
		}
		return kept[left].leaveAt.After(kept[right].leaveAt)
	})
	return kept
}

func dominates(candidate, other DepartureOption) bool {
	noWorse := candidate.Duration() <= other.Duration() &&
		!candidate.leaveAt.Before(other.leaveAt) &&
		!candidate.Arrival().After(other.Arrival())
	strictlyBetter := candidate.Duration() < other.Duration() ||
		candidate.leaveAt.After(other.leaveAt) ||
		candidate.Arrival().Before(other.Arrival())
	return noWorse && strictlyBetter
}
