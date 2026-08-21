package app

import (
	"context"
	"time"

	"mind-the-time/internal/domain"
)

// maxPlanDuration bounds how far past the departure window a timetable must be
// read: a train leaving at the end of the window still has to reach the
// destination, and its arrival lies outside that window.
const maxPlanDuration = 3 * time.Hour

type DeparturePlanner struct {
	timetables TimetableProvider
	limiter    MissLimiter
}

func NewDeparturePlanner(timetables TimetableProvider, limiter MissLimiter) DeparturePlanner {
	return DeparturePlanner{timetables: timetables, limiter: limiter}
}

// Assembling a plan request is application work: the HTTP layer would otherwise
// have to know how a pattern and personal timings are derived from a commute.
func PlanRequestForSelection(
	selected []domain.Itinerary,
	commute domain.Commute,
	earliest time.Time,
	window time.Duration,
) (PlanRequest, error) {
	if len(selected) == 0 {
		return PlanRequest{}, domain.ErrInvalidRoutePattern
	}
	pattern, err := selected[0].RoutePattern()
	if err != nil {
		return PlanRequest{}, err
	}
	return PlanRequestForPattern(pattern, commute, earliest, window), nil
}

// PlanRequestForPattern prepares a runtime-bound timetable request for an
// already saved exact pattern. The pattern contains no date or window state.
func PlanRequestForPattern(
	pattern domain.RoutePattern,
	commute domain.Commute,
	earliest time.Time,
	window time.Duration,
) PlanRequest {
	return PlanRequest{
		Pattern:  pattern.Clone(),
		Timings:  domain.NewPersonalTimings(commute.MaximumInitialWalk(), commute.SaintLazareMinimumTransferTime()),
		Earliest: earliest,
		Window:   window,
	}
}

func (p DeparturePlanner) Plan(ctx context.Context, request PlanRequest) ([]domain.DepartureOption, error) {
	window, err := domain.NewDepartureWindow(request.Earliest, request.Window)
	if err != nil {
		return nil, err
	}
	if !p.limiter.Allow() {
		return nil, ErrRateLimited
	}
	snapshot, err := p.timetables.TimetableSnapshot(ctx, request.Pattern, request.Earliest, request.Window+maxPlanDuration)
	if err != nil {
		return nil, err
	}
	return domain.PlanDepartureOptions(snapshot, request.Timings, window), nil
}
