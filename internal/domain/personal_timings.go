package domain

import "time"

// PersonalTimings are facts about the traveller, not filters on results: how
// long they take to reach the first vehicle and to change between two.
type PersonalTimings struct {
	accessToFirstBoard TransferBuffer
	interchange        TransferBuffer
}

func NewPersonalTimings(accessToFirstBoard, interchange TransferBuffer) PersonalTimings {
	return PersonalTimings{accessToFirstBoard: accessToFirstBoard, interchange: interchange}
}

func (t PersonalTimings) AccessToFirstBoard() TransferBuffer {
	return t.accessToFirstBoard
}

func (t PersonalTimings) Interchange() TransferBuffer {
	return t.interchange
}

func (t PersonalTimings) allowsInterchange(arrival, nextDeparture time.Time) bool {
	return !nextDeparture.Before(arrival.Add(t.interchange.duration))
}

func (t PersonalTimings) leaveAtFor(firstDeparture time.Time) time.Time {
	return firstDeparture.Add(-t.accessToFirstBoard.duration)
}
