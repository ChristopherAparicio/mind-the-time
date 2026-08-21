package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"

	"mind-the-time/internal/adapters"
	"mind-the-time/internal/app"
	"mind-the-time/internal/config"
	httpserver "mind-the-time/internal/http"
	"mind-the-time/internal/ui"
)

const (
	serverReadTimeout    = 15 * time.Second
	serverMaxHeaderBytes = 1 << 20
)

func main() {
	configuration, err := config.Load(os.Getenv)
	if err != nil {
		log.Printf("configuration error: %v", err)
		return
	}
	handler, err := buildServer(configuration, ui.Assets)
	if err != nil {
		log.Printf("startup error: %v", err)
		return
	}
	server := newHTTPServer(configuration, handler)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server stopped: %v", err)
	}
}

func newHTTPServer(configuration config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              configuration.Addr,
		Handler:           handler,
		ReadHeaderTimeout: configuration.HTTPTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      configuration.HTTPTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

func buildServer(configuration config.Config, assets fs.FS) (http.Handler, error) {
	store, err := adapters.NewFileStore(adapters.FileStoreSettings{Path: configuration.StorePath})
	if err != nil {
		return nil, fmt.Errorf("create file store: %w", err)
	}
	providerClient := &http.Client{Timeout: configuration.HTTPTimeout}
	if configuration.PRIMReplayDir != "" {
		if configuration.PRIMReplayStrict {
			providerClient.Transport = adapters.NewStrictReplayTransport(configuration.PRIMReplayDir, providerClient.Transport)
		} else {
			providerClient.Transport = adapters.NewReplayTransport(configuration.PRIMReplayDir, providerClient.Transport)
		}
	}
	provider := adapters.NewPRIMClient(configuration.PRIMEndpoint, configuration.PRIMAPIKey, providerClient)
	cache := adapters.NewTTLRouteCache(configuration.CacheMaxEntries, configuration.CacheTTL)
	limiter := adapters.NewRateMissLimiter(rate.Limit(configuration.RatePerSecond), configuration.RateBurst)
	routes := app.NewRouteService(provider, cache, limiter)
	planner := app.NewDeparturePlanner(provider, limiter)
	server, err := httpserver.NewServer(assets, httpserver.Dependencies{
		Owners:            store.Owners(),
		Commutes:          store.Commutes(),
		Routes:            routes,
		Planner:           planner,
		CookieName:        configuration.CookieName,
		CookieSecure:      configuration.CookieSecure,
		ProviderAvailable: configuration.PRIMConfigured(),
		Now:               fixedClock(configuration.FixedNow),
	})
	if err != nil {
		return nil, fmt.Errorf("create HTTP server: %w", err)
	}
	return server.Handler(), nil
}

func fixedClock(moment time.Time) func() time.Time {
	if moment.IsZero() {
		return nil
	}
	return func() time.Time { return moment }
}
