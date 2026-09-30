package app

import (
	"context"
	"errors"
	"sort"
	"time"

	"mind-the-time/internal/domain"
)

var ErrSavedCombinationNotFound = errors.New("saved route combination not found")

type RoutePatternCandidate struct {
	Pattern domain.RoutePattern
}

// RoutePatternDiscovery evaluates every already-validated representative
// instant. Its output is suitable for persistence, but it never persists an
// evaluation date or departure window as part of a pattern.
type RoutePatternDiscovery struct {
	routes interface {
		Evaluate(context.Context, RouteRequest) ([]domain.Itinerary, error)
	}
}

func NewRoutePatternDiscovery(routes interface {
	Evaluate(context.Context, RouteRequest) ([]domain.Itinerary, error)
}) RoutePatternDiscovery {
	return RoutePatternDiscovery{routes: routes}
}

func (d RoutePatternDiscovery) Discover(ctx context.Context, commute domain.Commute, instants []time.Time) ([]RoutePatternCandidate, error) {
	candidates := make(map[string]RoutePatternCandidate)
	for _, instant := range instants {
		routes, err := d.routes.Evaluate(ctx, RouteRequest{Commute: commute, Departure: instant})
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			pattern, err := route.RoutePattern()
			if err != nil {
				return nil, err
			}
			fingerprint := pattern.Fingerprint()
			if fingerprint == "" {
				return nil, domain.ErrInvalidRoutePattern
			}
			if _, exists := candidates[fingerprint]; !exists {
				candidates[fingerprint] = RoutePatternCandidate{Pattern: pattern}
			}
		}
	}
	result := make([]RoutePatternCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Pattern.Fingerprint() < result[right].Pattern.Fingerprint()
	})
	return result, nil
}

type PatternPlanExecutor interface {
	Plan(context.Context, PlanRequest) ([]domain.DepartureOption, error)
}

type SavedCombinationOutcome uint8

const (
	SavedCombinationViable SavedCombinationOutcome = iota + 1
	SavedCombinationNoViableDeparture
	SavedCombinationNoTimetableCoverage
	SavedCombinationProviderUnavailable
)

type SavedCombinationPlan struct {
	Combination SavedRouteCombination
	Outcome     SavedCombinationOutcome
	Options     []domain.DepartureOption
}

// SavedCombinationRuntimePlanner evaluates only patterns identified by saved,
// opaque IDs. It does not call route discovery, so results are scoped to the
// caller's selection and each exact pattern receives its own timetable plan.
type SavedCombinationRuntimePlanner struct {
	planner PatternPlanExecutor
}

func NewSavedCombinationRuntimePlanner(planner PatternPlanExecutor) SavedCombinationRuntimePlanner {
	return SavedCombinationRuntimePlanner{planner: planner}
}

func (p SavedCombinationRuntimePlanner) Plan(
	ctx context.Context,
	record CommuteRecord,
	selected []SavedCombinationID,
	earliest time.Time,
	window time.Duration,
) ([]SavedCombinationPlan, error) {
	byID := make(map[SavedCombinationID]SavedRouteCombination, len(record.SavedCombinations))
	for _, combination := range record.SavedCombinations {
		byID[combination.ID] = cloneSavedRouteCombination(combination)
	}
	plans := make([]SavedCombinationPlan, 0, len(selected))
	seen := make(map[SavedCombinationID]struct{}, len(selected))
	for _, id := range selected {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		combination, exists := byID[id]
		if !exists {
			return nil, ErrSavedCombinationNotFound
		}
		if combination.Pattern.Fingerprint() == "" {
			return nil, domain.ErrInvalidRoutePattern
		}
		options, err := p.planner.Plan(ctx, PlanRequestForPattern(combination.Pattern, record.Commute, earliest, window))
		if errors.Is(err, ErrNoTimetableCoverage) {
			plans = append(plans, SavedCombinationPlan{Combination: combination, Outcome: SavedCombinationNoTimetableCoverage})
			continue
		}
		if errors.Is(err, ErrProviderUnavailable) {
			plans = append(plans, SavedCombinationPlan{Combination: combination, Outcome: SavedCombinationProviderUnavailable})
			continue
		}
		if err != nil {
			return nil, err
		}
		outcome := SavedCombinationNoViableDeparture
		if len(options) > 0 {
			outcome = SavedCombinationViable
		}
		plans = append(plans, SavedCombinationPlan{Combination: combination, Outcome: outcome, Options: append([]domain.DepartureOption(nil), options...)})
	}
	sort.SliceStable(plans, func(left, right int) bool {
		return savedCombinationPlanLess(plans[left], plans[right])
	})
	return plans, nil
}

func cloneSavedRouteCombination(combination SavedRouteCombination) SavedRouteCombination {
	combination.Pattern = combination.Pattern.Clone()
	return combination
}

func savedCombinationPlanLess(left, right SavedCombinationPlan) bool {
	if left.Outcome != SavedCombinationViable || right.Outcome != SavedCombinationViable {
		if left.Outcome != right.Outcome {
			return left.Outcome < right.Outcome
		}
		return left.Combination.ID < right.Combination.ID
	}
	leftBest := latestViableDeparture(left.Options)
	rightBest := latestViableDeparture(right.Options)
	if !leftBest.LeaveAt().Equal(rightBest.LeaveAt()) {
		return leftBest.LeaveAt().After(rightBest.LeaveAt())
	}
	if !leftBest.Arrival().Equal(rightBest.Arrival()) {
		return leftBest.Arrival().Before(rightBest.Arrival())
	}
	if leftBest.Duration() != rightBest.Duration() {
		return leftBest.Duration() < rightBest.Duration()
	}
	return left.Combination.ID < right.Combination.ID
}

func latestViableDeparture(options []domain.DepartureOption) domain.DepartureOption {
	best := options[0]
	for _, option := range options[1:] {
		if option.LeaveAt().After(best.LeaveAt()) ||
			(option.LeaveAt().Equal(best.LeaveAt()) && option.Arrival().Before(best.Arrival())) ||
			(option.LeaveAt().Equal(best.LeaveAt()) && option.Arrival().Equal(best.Arrival()) && option.Duration() < best.Duration()) {
			best = option
		}
	}
	return best
}
