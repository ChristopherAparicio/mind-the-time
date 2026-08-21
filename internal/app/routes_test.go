package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mind-the-time/internal/domain"
)

func validCommuteInput() CommuteInput {
	return CommuteInput{
		Name:                       "Retour à la maison",
		Origin:                     "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "5",
		Weekdays: []time.Weekday{
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
		},
	}
}

func TestRouteServiceReadsCacheBeforeAcquiringMissLimit(t *testing.T) {
	_, commute, err := NewCommute(validCommuteInput())
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	request := RouteRequest{Commute: commute, Departure: time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)}
	cache := &routeCacheStub{values: make(map[string][]domain.Itinerary)}
	cache.Set(request.CacheKey(), []domain.Itinerary{})
	limiter := &limiterStub{allow: false}
	provider := &providerStub{}
	service := NewRouteService(provider, cache, limiter)

	_, err = service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if limiter.calls != 0 {
		t.Errorf("limiter calls = %d, want 0 for cache hit", limiter.calls)
	}
	if provider.calls != 0 {
		t.Errorf("provider calls = %d, want 0 for cache hit", provider.calls)
	}
}

func TestRouteServiceReturnsRateDeniedForCacheMiss(t *testing.T) {
	_, commute, err := NewCommute(validCommuteInput())
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	service := NewRouteService(&providerStub{}, &routeCacheStub{values: make(map[string][]domain.Itinerary)}, &limiterStub{allow: false})
	_, err = service.Evaluate(context.Background(), RouteRequest{Commute: commute, Departure: time.Now()})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("Evaluate() error = %v, want ErrRateLimited", err)
	}
}

func TestRouteServiceReturnsAndCachesEligibleCandidatesRankedByDuration(t *testing.T) {
	_, commute, err := NewCommute(validCommuteInput())
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	base := time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)
	longerDuration := itinerary(t,
		transitLeg(t, "origin", "destination", base, base.Add(25*time.Minute)),
	)
	longProviderWalk := itinerary(t,
		walkingLeg(t, base, base.Add(5*time.Minute+time.Second)),
		transitLeg(t, "origin", "destination", base.Add(5*time.Minute+time.Second), base.Add(25*time.Minute)),
	)
	shorterDuration := itinerary(t,
		transitLeg(t, "origin", "destination", base.Add(15*time.Minute), base.Add(30*time.Minute)),
	)
	request := RouteRequest{Commute: commute, Departure: base}
	cache := &routeCacheStub{values: make(map[string][]domain.Itinerary)}
	service := NewRouteService(candidateProvider{routes: []domain.Itinerary{longerDuration, longProviderWalk, shorterDuration}}, cache, &limiterStub{allow: true})

	routes, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got, want := len(routes), 3; got != want {
		t.Fatalf("Evaluate() returned %d routes, want %d; a provider walk longer than the traveller's own pace must not be discarded", got, want)
	}
	if routes[0].Duration() != shorterDuration.Duration() {
		t.Errorf("Evaluate() routes are not ranked by duration: %#v", routes)
	}
	cached, ok := cache.Get(request.CacheKey())
	if !ok || len(cached) != 3 {
		t.Errorf("cache = %#v, %t; want three ranked routes", cached, ok)
	}
}

type providerStub struct{ calls int }

func (p *providerStub) Routes(context.Context, RouteRequest) ([]domain.Itinerary, error) {
	p.calls++
	return nil, nil
}

type limiterStub struct {
	allow bool
	calls int
}

func (l *limiterStub) Allow() bool {
	l.calls++
	return l.allow
}

type routeCacheStub struct{ values map[string][]domain.Itinerary }

func (c *routeCacheStub) Get(key string) ([]domain.Itinerary, bool) {
	value, ok := c.values[key]
	return value, ok
}

func (c *routeCacheStub) Set(key string, value []domain.Itinerary) { c.values[key] = value }

type candidateProvider struct{ routes []domain.Itinerary }

func (p candidateProvider) Routes(context.Context, RouteRequest) ([]domain.Itinerary, error) {
	return p.routes, nil
}

func itinerary(t *testing.T, legs ...domain.ItineraryLeg) domain.Itinerary {
	t.Helper()
	itinerary, err := domain.NewItinerary(legs)
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}
	return itinerary
}

func walkingLeg(t *testing.T, departure, arrival time.Time) domain.ItineraryLeg {
	t.Helper()
	leg, err := domain.NewWalkingLeg(departure, arrival)
	if err != nil {
		t.Fatalf("NewWalkingLeg() error = %v", err)
	}
	return leg
}

func transitLeg(t *testing.T, from, to string, departure, arrival time.Time) domain.ItineraryLeg {
	t.Helper()
	fromStation, err := domain.NewStationID(from)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", from, err)
	}
	toStation, err := domain.NewStationID(to)
	if err != nil {
		t.Fatalf("NewStationID(%q) error = %v", to, err)
	}
	leg, err := domain.NewTransitLeg(fromStation, toStation, departure, arrival)
	if err != nil {
		t.Fatalf("NewTransitLeg() error = %v", err)
	}
	return leg
}
