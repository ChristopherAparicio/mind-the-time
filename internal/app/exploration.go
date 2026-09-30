package app

import (
	"errors"
	"time"

	"mind-the-time/internal/domain"
)

var ErrInvalidExplorationWindow = errors.New("exploration window must be increasing")

// ExplorationInstants samples a custom window at its start, midpoint and end.
func ExplorationInstants(serviceDate time.Time, start, end domain.DepartureTime, loc *time.Location) ([]time.Time, error) {
	if !start.Before(end) {
		return nil, ErrInvalidExplorationWindow
	}
	startTime := time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), start.Hour(), start.Minute(), 0, 0, loc)
	endTime := time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), end.Hour(), end.Minute(), 0, 0, loc)

	midTime := startTime.Add(endTime.Sub(startTime) / 2)

	instants := []time.Time{startTime}
	if midTime.After(startTime) && midTime.Before(endTime) {
		instants = append(instants, midTime)
	}
	instants = append(instants, endTime)
	return instants, nil
}

// DefaultExplorationInstants samples a service date at morning, noon and
// evening, enough to surface the route patterns the network actually serves.
func DefaultExplorationInstants(serviceDate time.Time, loc *time.Location) []time.Time {
	return []time.Time{
		time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), 8, 0, 0, 0, loc),
		time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), 12, 0, 0, 0, loc),
		time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), 18, 0, 0, 0, loc),
	}
}
