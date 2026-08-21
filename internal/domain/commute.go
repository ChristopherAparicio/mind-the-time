package domain

import "errors"

var ErrInvalidCommute = errors.New("invalid commute")

type Commute struct {
	origin                         Place
	destination                    Place
	schedule                       WeeklySchedule
	maximumInitialWalk             TransferBuffer
	saintLazareMinimumTransferTime TransferBuffer
	preferredLines                 PreferredLines
}

func NewCommute(origin, destination Place, schedule WeeklySchedule, maximumInitialWalk, saintLazareMinimumTransferTime TransferBuffer, preferredLines PreferredLines) (Commute, error) {
	if !origin.valid() || !destination.valid() || !schedule.isValid() || !maximumInitialWalk.valid() || !saintLazareMinimumTransferTime.valid() {
		return Commute{}, ErrInvalidCommute
	}
	return Commute{
		origin:                         origin,
		destination:                    destination,
		schedule:                       schedule,
		maximumInitialWalk:             maximumInitialWalk,
		saintLazareMinimumTransferTime: saintLazareMinimumTransferTime,
		preferredLines:                 preferredLines,
	}, nil
}

func (c Commute) Origin() Place {
	return c.origin
}

func (c Commute) Destination() Place {
	return c.destination
}

func (c Commute) Schedule() WeeklySchedule {
	return c.schedule
}

func (c Commute) MaximumInitialWalk() TransferBuffer {
	return c.maximumInitialWalk
}

func (c Commute) SaintLazareMinimumTransferTime() TransferBuffer {
	return c.saintLazareMinimumTransferTime
}

func (c Commute) PreferredLines() PreferredLines {
	return c.preferredLines
}

func (c Commute) valid() bool {
	return c.origin.valid() && c.destination.valid() && c.schedule.isValid() && c.maximumInitialWalk.valid() && c.saintLazareMinimumTransferTime.valid()
}
