package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"mind-the-time/internal/domain"
)

type RouteService struct {
	provider RouteProvider
	cache    RouteCache
	limiter  MissLimiter
}

func NewRouteService(provider RouteProvider, cache RouteCache, limiter MissLimiter) RouteService {
	return RouteService{provider: provider, cache: cache, limiter: limiter}
}

func (s RouteService) Evaluate(ctx context.Context, request RouteRequest) ([]domain.Itinerary, error) {
	key := request.CacheKey()
	if routes, ok := s.cache.Get(key); ok {
		return routes, nil
	}
	if !s.limiter.Allow() {
		return nil, ErrRateLimited
	}
	routes, err := s.provider.Routes(ctx, request)
	if err != nil {
		return nil, err
	}
	ranked := domain.RankCandidates(request.Commute, routes)
	s.cache.Set(key, ranked)
	return ranked, nil
}

func (r RouteRequest) CacheKey() string {
	var key strings.Builder
	appendCacheField(&key, r.Commute.Origin().Text())
	appendCacheField(&key, r.Commute.Destination().Text())
	appendCacheField(&key, r.Departure.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	appendCacheField(&key, r.Commute.Schedule().DepartureTime().String())
	for day := range 7 {
		appendCacheField(&key, strconv.FormatBool(r.Commute.Schedule().IsActive(time.Weekday(day))))
	}
	appendCacheField(&key, r.Commute.MaximumInitialWalk().Duration().String())
	appendCacheField(&key, r.Commute.SaintLazareMinimumTransferTime().Duration().String())
	appendCacheField(&key, r.Commute.PreferredLines().Raw())
	return key.String()
}

func appendCacheField(key *strings.Builder, value string) {
	key.WriteString(strconv.Itoa(len(value)))
	key.WriteByte(':')
	key.WriteString(value)
}
