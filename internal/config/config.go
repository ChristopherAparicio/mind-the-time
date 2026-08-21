package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr            = "127.0.0.1:8080"
	defaultCookieName      = "mind_the_time_owner"
	defaultStorePath       = "data/mind-the-time.json"
	defaultHTTPTimeout     = 10 * time.Second
	defaultCacheTTL        = 5 * time.Minute
	defaultCacheMaxEntries = 128
	defaultRatePerSecond   = 10.0
	defaultRateBurst       = 50
)

type Config struct {
	Addr             string
	CookieName       string
	CookieSecure     bool
	StorePath        string
	PRIMEndpoint     string
	PRIMAPIKey       string
	PRIMReplayDir    string
	PRIMReplayStrict bool
	FixedNow         time.Time
	HTTPTimeout      time.Duration
	CacheTTL         time.Duration
	CacheMaxEntries  int
	RatePerSecond    float64
	RateBurst        int
}

func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("environment reader is required")
	}
	dotEnv := loadDotEnv(".env")
	envGetter := func(key string) string {
		val := getenv(key)
		if val == "" {
			val = dotEnv[key]
		}
		return val
	}
	cookieSecure, err := optionalBool(envGetter("COOKIE_SECURE"), true)
	if err != nil {
		return Config{}, fmt.Errorf("COOKIE_SECURE: %w", err)
	}
	primReplayStrict, err := optionalBool(envGetter("PRIM_REPLAY_STRICT"), false)
	if err != nil {
		return Config{}, fmt.Errorf("PRIM_REPLAY_STRICT: %w", err)
	}
	httpTimeout, err := optionalDuration(envGetter("HTTP_TIMEOUT"), defaultHTTPTimeout)
	if err != nil || httpTimeout <= 0 {
		return Config{}, fmt.Errorf("HTTP_TIMEOUT must be a positive duration")
	}
	cacheTTL, err := optionalDuration(envGetter("CACHE_TTL"), defaultCacheTTL)
	if err != nil || cacheTTL <= 0 {
		return Config{}, fmt.Errorf("CACHE_TTL must be a positive duration")
	}
	cacheMaxEntries, err := optionalPositiveInt(envGetter("CACHE_MAX_ENTRIES"), defaultCacheMaxEntries)
	if err != nil {
		return Config{}, fmt.Errorf("CACHE_MAX_ENTRIES: %w", err)
	}
	ratePerSecond, err := optionalPositiveFloat(envGetter("ROUTE_RATE_PER_SECOND"), defaultRatePerSecond)
	if err != nil {
		return Config{}, fmt.Errorf("ROUTE_RATE_PER_SECOND: %w", err)
	}
	rateBurst, err := optionalPositiveInt(envGetter("ROUTE_RATE_BURST"), defaultRateBurst)
	if err != nil {
		return Config{}, fmt.Errorf("ROUTE_RATE_BURST: %w", err)
	}
	return Config{
		Addr:             optionalString(envGetter("HTTP_ADDR"), optionalString(envGetter("ADDR"), defaultAddr)),
		CookieName:       optionalString(envGetter("COOKIE_NAME"), defaultCookieName),
		CookieSecure:     cookieSecure,
		StorePath:        optionalString(envGetter("STORE_PATH"), defaultStorePath),
		PRIMEndpoint:     strings.TrimSpace(envGetter("IDFM_PRIM_ROUTE_URL")),
		PRIMAPIKey:       strings.TrimSpace(envGetter("IDFM_PRIM_API_KEY")),
		PRIMReplayDir:    strings.TrimSpace(envGetter("PRIM_REPLAY_DIR")),
		PRIMReplayStrict: primReplayStrict,
		FixedNow:         parseFixedNow(envGetter("NOW_OVERRIDE")),
		HTTPTimeout:      httpTimeout,
		CacheTTL:         cacheTTL,
		CacheMaxEntries:  cacheMaxEntries,
		RatePerSecond:    ratePerSecond,
		RateBurst:        rateBurst,
	}, nil
}

func loadDotEnv(path string) map[string]string {
	env := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return env
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			env[key] = val
		}
	}
	return env
}

func (c Config) PRIMConfigured() bool {
	return c.PRIMConfigurationReady()
}

func (c Config) PRIMConfigurationReady() bool {
	if c.PRIMAPIKey == "" {
		return false
	}
	endpoint, err := url.ParseRequestURI(c.PRIMEndpoint)
	return err == nil && endpoint.Scheme == "https" && endpoint.Host != ""
}

func optionalString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func optionalBool(value string, fallback bool) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}

func optionalDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func optionalPositiveInt(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return parsed, nil
}

func optionalPositiveFloat(value string, fallback float64) (float64, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return parsed, nil
}

// A frozen clock keeps recorded provider responses replayable: their URLs carry
// the requested date, so a real clock invalidates every fixture at midnight.
func parseFixedNow(value string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
