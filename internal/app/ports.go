package app

import (
	"context"
	"errors"
	"time"

	"mind-the-time/internal/domain"
)

var (
	ErrCommuteNotFound             = errors.New("commute not found")
	ErrCommuteCapacityExceeded     = errors.New("commute capacity exceeded")
	ErrProviderUnavailable         = errors.New("route provider unavailable")
	ErrProviderUnsupportedContract = errors.New("route provider response contract unsupported")
	ErrNoTimetableCoverage         = errors.New("no timetable coverage for the requested leg and window")
	ErrRateLimited                 = errors.New("route evaluation rate limited")
	ErrOwnerCapacityExceeded       = errors.New("owner capacity exceeded")
)

type OwnerID string

type CommuteID string

type SavedCombinationID string

type SavedRouteCombination struct {
	ID      SavedCombinationID
	Pattern domain.RoutePattern
}

type OwnerStore interface {
	Create(context.Context) (OwnerID, error)
	Touch(context.Context, OwnerID) (bool, error)
	Exists(context.Context, OwnerID) (bool, error)
	Delete(context.Context, OwnerID) error
}

type CommuteRecord struct {
	ID                CommuteID
	Name              domain.CommuteName
	Commute           domain.Commute
	SavedCombinations []SavedRouteCombination
}

type CommuteStore interface {
	List(context.Context, OwnerID) ([]CommuteRecord, error)
	Create(context.Context, OwnerID, domain.CommuteName, domain.Commute) (CommuteRecord, error)
	Get(context.Context, OwnerID, CommuteID) (CommuteRecord, error)
	Update(context.Context, OwnerID, CommuteID, domain.CommuteName, domain.Commute) (CommuteRecord, error)
	Delete(context.Context, OwnerID, CommuteID) error
	ReplaceSavedRouteCombinations(context.Context, OwnerID, CommuteID, []domain.RoutePattern) (CommuteRecord, error)
}

type RouteRequest struct {
	Commute   domain.Commute
	Departure time.Time
}

type RouteProvider interface {
	Routes(context.Context, RouteRequest) ([]domain.Itinerary, error)
}

type RouteCache interface {
	Get(string) ([]domain.Itinerary, bool)
	Set(string, []domain.Itinerary)
}

type MissLimiter interface {
	Allow() bool
}

type PlanRequest struct {
	Pattern  domain.RoutePattern
	Timings  domain.PersonalTimings
	Earliest time.Time
	Window   time.Duration
}

type TimetableProvider interface {
	TimetableSnapshot(context.Context, domain.RoutePattern, time.Time, time.Duration) (domain.TimetableSnapshot, error)
}
