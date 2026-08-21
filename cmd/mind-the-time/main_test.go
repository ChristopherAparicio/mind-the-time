package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mind-the-time/internal/config"
	"mind-the-time/internal/ui"
)

func TestBuildServerServesHealthWithoutPRIMConfiguration(t *testing.T) {
	server, err := buildServer(config.Config{
		Addr:            ":8080",
		CookieName:      "test-session",
		StorePath:       filepath.Join(t.TempDir(), "state.json"),
		HTTPTimeout:     time.Second,
		CacheTTL:        time.Minute,
		CacheMaxEntries: 8,
		RatePerSecond:   1,
		RateBurst:       1,
	}, fstest.MapFS{})
	if err != nil {
		t.Fatalf("buildServer() error = %v", err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", response.Code)
	}
}

func TestEmbeddedAssetsBuildServerOutsideProjectWorkingDirectory(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("Chdir(%q) cleanup error = %v", originalDirectory, err)
		}
	})
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir(temp dir) error = %v", err)
	}
	server, err := buildServer(config.Config{
		Addr:            ":8080",
		CookieName:      "test-session",
		StorePath:       filepath.Join(t.TempDir(), "state.json"),
		HTTPTimeout:     time.Second,
		CacheTTL:        time.Minute,
		CacheMaxEntries: 8,
		RatePerSecond:   1,
		RateBurst:       1,
	}, ui.Assets)
	if err != nil {
		t.Fatalf("buildServer() error = %v", err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/style.css", nil))
	if response.Code != http.StatusOK {
		t.Errorf("GET embedded style status = %d, want 200", response.Code)
	}
}

func TestBuildServerPersistsCommuteAcrossRestart(t *testing.T) {
	configuration := config.Config{
		Addr:            ":8080",
		CookieName:      "test-owner",
		StorePath:       filepath.Join(t.TempDir(), "state.json"),
		HTTPTimeout:     time.Second,
		CacheTTL:        time.Minute,
		CacheMaxEntries: 8,
		RatePerSecond:   1,
		RateBurst:       1,
	}
	first, err := buildServer(configuration, fstest.MapFS{})
	if err != nil {
		t.Fatalf("first buildServer() error = %v", err)
	}
	form := url.Values{
		"name":                  {"Bureau"},
		"origin":                {"Café Rivoli (10 Rue de Rivoli, 75004 Paris)"},
		"destination":           {"Cergy-le-Haut"},
		"departure_time":        {"18:00"},
		"weekday":               {"monday"},
		"maximum_initial_walk":  {"5"},
		"saint_lazare_transfer": {"7"},
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/commutes", strings.NewReader(form.Encode()))
	createRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	first.ServeHTTP(created, createRequest)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("first POST /commutes status = %d, want 303", created.Code)
	}
	location := created.Result().Header.Get("Location")
	if location == "" {
		t.Fatal("first POST /commutes did not return a location")
	}
	var ownerCookie *http.Cookie
	for _, cookie := range created.Result().Cookies() {
		if cookie.Name == configuration.CookieName {
			ownerCookie = cookie
			break
		}
	}
	if ownerCookie == nil {
		t.Fatal("first POST /commutes did not return an owner cookie")
	}

	second, err := buildServer(configuration, fstest.MapFS{})
	if err != nil {
		t.Fatalf("second buildServer() error = %v", err)
	}
	persistedRequest := httptest.NewRequest(http.MethodGet, location, nil)
	persistedRequest.AddCookie(ownerCookie)
	persisted := httptest.NewRecorder()
	second.ServeHTTP(persisted, persistedRequest)
	if persisted.Code != http.StatusOK {
		t.Errorf("GET persisted commute status = %d, want 200", persisted.Code)
	}
	cookies := persisted.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("GET persisted commute Set-Cookie = %v, want one renewed owner cookie", cookies)
	}
	if renewed := cookies[0]; renewed.Name != configuration.CookieName || renewed.Value != ownerCookie.Value {
		t.Errorf("GET persisted commute renewed cookie = %#v, want name %q and the same opaque value", renewed, configuration.CookieName)
	}
}

func TestNewHTTPServerHasFiniteRequestLimits(t *testing.T) {
	server := newHTTPServer(config.Config{Addr: ":8080", HTTPTimeout: time.Second}, http.NotFoundHandler())

	if server.ReadTimeout <= 0 {
		t.Errorf("ReadTimeout = %s, want a finite positive duration", server.ReadTimeout)
	}
	if server.MaxHeaderBytes <= 0 {
		t.Errorf("MaxHeaderBytes = %d, want a finite positive limit", server.MaxHeaderBytes)
	}
}
