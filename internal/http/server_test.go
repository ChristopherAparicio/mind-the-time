package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mind-the-time/internal/adapters"
	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
	"mind-the-time/internal/ui"
)

func TestHealthz(t *testing.T) {
	server := newTestServer(t, providerStub{})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Errorf("healthz = %d %q, want 200 ok", response.Code, response.Body.String())
	}
	if response.Result().Header.Get("Set-Cookie") != "" {
		t.Error("healthz unexpectedly created an owner")
	}
	if cacheControl := response.Result().Header.Get("Cache-Control"); cacheControl != "" {
		t.Errorf("healthz Cache-Control = %q, want empty", cacheControl)
	}
}

func TestOwnerRoutesSetNoStore(t *testing.T) {
	server := newTestServer(t, providerStub{})
	for _, test := range []struct {
		name   string
		method string
		path   string
		form   url.Values
		status int
	}{
		{name: "root", method: http.MethodGet, path: "/", status: http.StatusOK},
		{name: "protected commute", method: http.MethodGet, path: "/commutes/example", status: http.StatusNotFound},
		{name: "create redirect", method: http.MethodPost, path: "/commutes", form: validForm(), status: http.StatusSeeOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request *http.Request
			if test.form == nil {
				request = httptest.NewRequest(test.method, test.path, nil)
			} else {
				request = httptest.NewRequest(test.method, test.path, strings.NewReader(test.form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			response := httptest.NewRecorder()

			server.Handler().ServeHTTP(response, request)

			if response.Code != test.status {
				t.Errorf("status = %d, want %d", response.Code, test.status)
			}
			if cacheControl := response.Result().Header.Get("Cache-Control"); cacheControl != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cacheControl)
			}
		})
	}
}

func TestStaticAssetsDoNotSetNoStore(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, fstest.MapFS{
		"static/style.css": &fstest.MapFile{Data: []byte("body {}")},
	})
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/style.css", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET static asset status = %d, want 200", response.Code)
	}
	if cacheControl := response.Result().Header.Get("Cache-Control"); cacheControl != "" {
		t.Errorf("static asset Cache-Control = %q, want empty", cacheControl)
	}
}

func TestBlankInitialForm(t *testing.T) {
	server := newTestServer(t, providerStub{})
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	body := response.Body.String()
	for _, unexpected := range []string{"Café Rivoli (10 Rue de Rivoli, 75004 Paris)", "Cergy-le-Haut", "18:00"} {
		if strings.Contains(body, unexpected) {
			t.Errorf("GET / body unexpectedly contains %q: %s", unexpected, body)
		}
	}
	assertNoWeekdayPreChecked(t, body)
}

func TestEmbeddedUIShowsEvaluationActionForSavedCommute(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}

	page := client.get(created.Header().Get("Location"))
	if page.Code != http.StatusOK {
		t.Fatalf("GET saved commute status = %d, want 200", page.Code)
	}
	if !strings.Contains(page.Body.String(), "/explore") {
		t.Errorf("saved commute page does not expose an exploration action: %s", page.Body.String())
	}
}

func TestEmbeddedUIRendersValidationError(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	form := validForm()
	form.Set("departure_time", "not-a-time")

	response := newOwnerClient(t, server.Handler()).postForm("/commutes", form)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid commute status = %d, want 400", response.Code)
	}
	if !strings.Contains(response.Body.String(), "heure de départ est invalide.") {
		t.Errorf("invalid commute response does not render its validation error: %s", response.Body.String())
	}
}

func TestEmbeddedUIOwnsItsProviderUnavailableState(t *testing.T) {
	server := newTestServerWithAssets(t, unavailableProvider{}, ui.Assets)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	body := response.Body.String()
	if !strings.Contains(body, "ÉVALUATION PRIM INDISPONIBLE") {
		t.Errorf("embedded UI does not render its unavailable state: %s", body)
	}
	if !strings.Contains(body, `href="https://prim.iledefrance-mobilites.fr"`) || !strings.Contains(body, "IDFM_PRIM_API_KEY") {
		t.Errorf("embedded UI does not tell how to obtain and configure a PRIM key: %s", body)
	}
	if strings.Contains(body, "Route provider is unavailable.") {
		t.Errorf("embedded UI includes fallback unavailable markup: %s", body)
	}
}

func TestServerRendersInjectedIndexTemplate(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, fstest.MapFS{
		"templates/index.html": &fstest.MapFile{Data: []byte("<!doctype html><p>custom {{.Origin}} {{.DepartureTime}}</p>")},
	})
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "custom  ") {
		t.Errorf("GET / did not render injected template: %s", response.Body.String())
	}
}

func TestServerAcceptsUIFormDaysField(t *testing.T) {
	server := newTestServer(t, providerStub{})
	form := validForm()
	form.Set("days", "monday")
	form.Del("weekday")

	response := newOwnerClient(t, server.Handler()).postForm("/commutes", form)
	if response.Code != http.StatusSeeOther {
		t.Errorf("POST /commutes with days status = %d, want 303", response.Code)
	}
}

func TestCommutesAreIsolatedByOwner(t *testing.T) {
	server := newTestServer(t, providerStub{})
	first := newOwnerClient(t, server.Handler())
	second := newOwnerClient(t, server.Handler())

	response := first.postForm("/commutes", validForm())
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", response.Code)
	}
	location := response.Header().Get("Location")
	if location == "" {
		t.Fatal("POST /commutes did not return a location")
	}

	response = second.get(location)
	if response.Code != http.StatusNotFound {
		t.Errorf("second owner GET %s status = %d, want 404", location, response.Code)
	}
}

func TestNamedCommuteCRUDRendersPersistedNames(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, fstest.MapFS{
		"templates/index.html": &fstest.MapFile{Data: []byte("<!doctype html><p>{{.Name}}|{{.FormAction}}|{{range .Commutes}}{{.Name}},{{end}}</p>")},
	})
	client := newOwnerClient(t, server.Handler())
	form := validForm()
	form.Set("name", "  Bureau  ")
	created := client.postForm("/commutes", form)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}
	location := created.Header().Get("Location")
	if location == "" {
		t.Fatal("POST /commutes did not return a location")
	}
	page := client.get(location)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Bureau|"+location) {
		t.Errorf("GET selected commute = %d %q, want normalized persisted name and update action", page.Code, page.Body.String())
	}
	page = client.get("/")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Bureau,") {
		t.Errorf("GET / = %d %q, want persisted commute in list", page.Code, page.Body.String())
	}
	form.Set("name", "Domicile")
	updated := client.postForm(location, form)
	if updated.Code != http.StatusSeeOther || updated.Header().Get("Location") != location {
		t.Errorf("POST %s = %d %q, want 303 to selected commute", location, updated.Code, updated.Header().Get("Location"))
	}
	page = client.get(location)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Domicile|"+location) {
		t.Errorf("GET updated commute = %d %q, want updated persisted name", page.Code, page.Body.String())
	}
	deleted := client.postForm(location+"/delete", url.Values{})
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/" {
		t.Errorf("POST delete = %d %q, want 303 /", deleted.Code, deleted.Header().Get("Location"))
	}
	if response := client.get(location); response.Code != http.StatusNotFound {
		t.Errorf("GET deleted commute status = %d, want 404", response.Code)
	}
}

func TestProtectedCommuteRoutesAreScopedToOwner(t *testing.T) {
	server := newTestServer(t, providerStub{})
	first := newOwnerClient(t, server.Handler())
	created := first.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}
	location := created.Header().Get("Location")
	second := newOwnerClient(t, server.Handler())
	if response := second.get("/"); response.Code != http.StatusOK {
		t.Fatalf("second owner GET / status = %d, want 200", response.Code)
	}
	for name, request := range map[string]*httptest.ResponseRecorder{
		"read":     second.get(location),
		"update":   second.postForm(location, validForm()),
		"delete":   second.postForm(location+"/delete", url.Values{}),
		"evaluate": second.postForm(location+"/evaluate", url.Values{}),
	} {
		if request.Code != http.StatusNotFound {
			t.Errorf("cross-owner %s status = %d, want 404", name, request.Code)
		}
	}
	if response := first.get(location); response.Code != http.StatusOK {
		t.Errorf("first owner GET commute status = %d, want 200", response.Code)
	}
}

func TestOwnerCookieAttributesAndTouch(t *testing.T) {
	owners := &trackingOwnerStore{OwnerStore: adapters.NewMemoryOwners()}
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	server, err := NewServer(fstest.MapFS{}, Dependencies{
		Owners:       owners,
		Commutes:     commutes,
		Routes:       app.NewRouteService(providerStub{}, noopCache{}, allowLimiter{}),
		CookieName:   "test-owner",
		CookieSecure: true,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	client := newOwnerClient(t, server.Handler())
	created := client.get("/")
	if created.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", created.Code)
	}
	if client.cookie == nil || !client.cookie.HttpOnly || client.cookie.SameSite != http.SameSiteLaxMode || client.cookie.Path != "/" || !client.cookie.Secure {
		t.Errorf("owner cookie = %#v, want HttpOnly SameSite=Lax Path=/ Secure", client.cookie)
	}
	assertOwnerCookieLifetime(t, client.cookie, now)
	createdCookie := client.cookie
	now = now.Add(time.Hour)
	reused := newOwnerClient(t, server.Handler())
	reused.cookie = createdCookie
	response := reused.get("/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / with owner cookie status = %d, want 200", response.Code)
	}
	if len(response.Result().Cookies()) != 1 {
		t.Fatalf("GET / with owner cookie Set-Cookie = %v, want one renewed cookie", response.Result().Cookies())
	}
	if reused.cookie == nil || reused.cookie.Value != createdCookie.Value {
		t.Error("GET / with owner cookie did not reuse the existing owner")
	}
	assertOwnerCookieLifetime(t, reused.cookie, now)
	if !reused.cookie.Expires.After(createdCookie.Expires) {
		t.Errorf("renewed cookie expiry = %v, want after %v", reused.cookie.Expires, createdCookie.Expires)
	}
	if owners.touches != 1 {
		t.Errorf("owner Touch calls = %d, want 1", owners.touches)
	}
}

func assertOwnerCookieLifetime(t *testing.T, cookie *http.Cookie, now time.Time) {
	t.Helper()
	if cookie == nil {
		t.Fatal("owner cookie is nil")
	}
	wantMaxAge := int(ownerCookieLifetime / time.Second)
	if cookie.MaxAge != wantMaxAge {
		t.Errorf("owner cookie MaxAge = %d, want %d", cookie.MaxAge, wantMaxAge)
	}
	wantExpires := now.Add(ownerCookieLifetime)
	if difference := cookie.Expires.Sub(wantExpires); difference < -time.Second || difference > time.Second {
		t.Errorf("owner cookie Expires = %v, want near %v", cookie.Expires, wantExpires)
	}
}

func TestInvalidOwnerCookieIsReplacedOnlyOnPublicRoutes(t *testing.T) {
	server := newTestServer(t, providerStub{})
	for _, test := range []struct {
		method     string
		path       string
		form       url.Values
		wantStatus int
		wantCookie bool
	}{
		{http.MethodGet, "/", nil, http.StatusOK, true},
		{http.MethodPost, "/commutes", validForm(), http.StatusSeeOther, true},
		{http.MethodGet, "/commutes/example", nil, http.StatusNotFound, false},
		{http.MethodPost, "/commutes/example", validForm(), http.StatusNotFound, false},
		{http.MethodPost, "/commutes/example/delete", url.Values{}, http.StatusNotFound, false},
		{http.MethodPost, "/commutes/example/evaluate", url.Values{}, http.StatusNotFound, false},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			var request *http.Request
			if test.form == nil {
				request = httptest.NewRequest(test.method, test.path, nil)
			} else {
				request = httptest.NewRequest(test.method, test.path, strings.NewReader(test.form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			request.AddCookie(&http.Cookie{Name: "test-owner", Value: "invalid-owner"})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, test.wantStatus)
			}
			cookies := response.Result().Cookies()
			if gotCookie := len(cookies) > 0; gotCookie != test.wantCookie {
				t.Errorf("Set-Cookie = %v, want cookie present %t", cookies, test.wantCookie)
			}
		})
	}
}

func TestExpiredOwnerCookieIsNotRenewedOrUsed(t *testing.T) {
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	newServer := func(t *testing.T) (*Server, *adapters.MemoryOwners) {
		t.Helper()
		owners := adapters.NewMemoryOwners(adapters.MemoryRetention{MaxEntries: 1, TTL: time.Hour, Now: func() time.Time { return now }})
		commutes, err := adapters.NewMemoryCommutes(owners)
		if err != nil {
			t.Fatalf("NewMemoryCommutes() error = %v", err)
		}
		server, err := NewServer(fstest.MapFS{}, Dependencies{
			Owners:     owners,
			Commutes:   commutes,
			Routes:     app.NewRouteService(providerStub{}, noopCache{}, allowLimiter{}),
			CookieName: "test-owner",
			Now:        func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("NewServer() error = %v", err)
		}
		return server, owners
	}

	t.Run("public route replaces stale owner", func(t *testing.T) {
		server, owners := newServer(t)
		stale, err := owners.Create(context.Background())
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		now = now.Add(time.Hour)
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: "test-owner", Value: string(stale)})
		response := httptest.NewRecorder()

		server.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", response.Code)
		}
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Value == string(stale) {
			t.Fatalf("GET / Set-Cookie = %v, want one fresh owner cookie", cookies)
		}
		assertOwnerCookieLifetime(t, cookies[0], now)
	})

	t.Run("protected route does not create owner", func(t *testing.T) {
		now = time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
		server, owners := newServer(t)
		stale, err := owners.Create(context.Background())
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		now = now.Add(time.Hour)
		request := httptest.NewRequest(http.MethodGet, "/commutes/example", nil)
		request.AddCookie(&http.Cookie{Name: "test-owner", Value: string(stale)})
		response := httptest.NewRecorder()

		server.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Errorf("GET protected route status = %d, want 404", response.Code)
		}
		if cookies := response.Result().Cookies(); len(cookies) != 0 {
			t.Errorf("GET protected route Set-Cookie = %v, want none", cookies)
		}
		if _, err := owners.Create(context.Background()); err != nil {
			t.Errorf("Create() after protected route error = %v, want capacity available", err)
		}
	})
}

func TestMissingPRIMConfigurationRendersUnavailableAndEvaluationReturns503(t *testing.T) {
	server := newTestServer(t, unavailableProvider{})
	client := newOwnerClient(t, server.Handler())

	response := client.get("/")
	if !strings.Contains(response.Body.String(), "L'évaluation PRIM est indisponible dans cette version.") {
		t.Errorf("GET / did not render unavailable state: %s", response.Body.String())
	}
	response = client.postForm("/commutes", validForm())
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d", response.Code)
	}
	response = client.postForm(response.Header().Get("Location")+"/evaluate", url.Values{})
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("POST evaluate status = %d, want 503", response.Code)
	}
}

func TestInvalidInputReturns400(t *testing.T) {
	server := newTestServer(t, providerStub{})
	form := validForm()
	form.Set("departure_time", "wrong")

	response := newOwnerClient(t, server.Handler()).postForm("/commutes", form)
	if response.Code != http.StatusBadRequest {
		t.Errorf("POST invalid commute status = %d, want 400", response.Code)
	}
}

func TestInvalidCommuteNameReturns400(t *testing.T) {
	server := newTestServer(t, providerStub{})
	form := validForm()
	form.Set("name", " \t")

	response := newOwnerClient(t, server.Handler()).postForm("/commutes", form)
	if response.Code != http.StatusBadRequest {
		t.Errorf("POST invalid named commute status = %d, want 400", response.Code)
	}
}

func TestEvaluationRejectsInactiveScheduleDate(t *testing.T) {
	server := newTestServer(t, providerStub{})
	client := newOwnerClient(t, server.Handler())
	response := client.postForm("/commutes", validForm())
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d", response.Code)
	}

	response = client.postForm(response.Header().Get("Location")+"/evaluate", url.Values{"date": {"2026-08-01"}})
	if response.Code != http.StatusBadRequest {
		t.Errorf("POST evaluate Saturday status = %d, want 400", response.Code)
	}
}

func TestStaticAssetsAreServedWithExpectedContentTypes(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, fstest.MapFS{
		"static/style.css":     &fstest.MapFile{Data: []byte("body {}")},
		"static/manifest.json": &fstest.MapFile{Data: []byte(`{"name":"Mind the Time"}`)},
		"static/sw.js":         &fstest.MapFile{Data: []byte("self.addEventListener('install', () => {})")},
	})
	for _, test := range []struct {
		path        string
		contentType string
	}{
		{path: "/static/style.css", contentType: "text/css"},
		{path: "/static/manifest.json", contentType: "application/json"},
		{path: "/static/sw.js", contentType: "text/javascript"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", test.path, response.Code)
			}
			if contentType := response.Result().Header.Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
				t.Errorf("GET %s Content-Type = %q, want prefix %q", test.path, contentType, test.contentType)
			}
		})
	}
}

func TestRootServiceWorkerIsServedWithJavaScriptContentType(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, fstest.MapFS{
		"static/sw.js": &fstest.MapFile{Data: []byte("self.addEventListener('install', () => {})")},
	})
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sw.js", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET /sw.js status = %d, want 200", response.Code)
	}
	if contentType := response.Result().Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/javascript") {
		t.Errorf("GET /sw.js Content-Type = %q, want JavaScript", contentType)
	}
}

func TestUnknownAndMalformedRoutesDoNotCreateOwners(t *testing.T) {
	server := newTestServer(t, providerStub{})
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/unknown"},
		{http.MethodGet, "/commutes/"},
		{http.MethodGet, "/commutes/example/extra"},
		{http.MethodGet, "/commutes//evaluate"},
		{http.MethodGet, "/commutes/example"},
		{http.MethodPost, "/commutes/example"},
		{http.MethodPost, "/commutes/example/delete"},
		{http.MethodPost, "/commutes/example/evaluate"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("%s %s status = %d, want 404", test.method, test.path, response.Code)
			}
			if cookies := response.Result().Cookies(); len(cookies) != 0 {
				t.Errorf("%s %s Set-Cookie = %v, want none", test.method, test.path, cookies)
			}
		})
	}
}

func TestOversizedCreateFormReturnsFrench413(t *testing.T) {
	server := newTestServer(t, providerStub{})
	request := httptest.NewRequest(http.MethodPost, "/commutes", strings.NewReader("origin="+strings.Repeat("x", 64*1024)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /commutes oversized form status = %d, want 413", response.Code)
	}
	if !strings.Contains(response.Body.String(), "La taille du formulaire dépasse la limite autorisée.") {
		t.Errorf("oversized create error is not French: %s", response.Body.String())
	}
}

func TestOversizedEvaluationFormReturnsFrench413(t *testing.T) {
	server := newTestServer(t, providerStub{})
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}
	request := httptest.NewRequest(http.MethodPost, created.Header().Get("Location")+"/evaluate", strings.NewReader("date="+strings.Repeat("x", 1024)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := client.do(request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST evaluate oversized form status = %d, want 413", response.Code)
	}
	if !strings.Contains(response.Body.String(), "La taille du formulaire dépasse la limite autorisée.") {
		t.Errorf("oversized evaluation error is not French: %s", response.Body.String())
	}
}

func TestServiceDateUsesParisLocalMidnight(t *testing.T) {
	now := time.Date(2026, time.January, 4, 23, 30, 0, 0, time.UTC)
	request := httptest.NewRequest(http.MethodPost, "/commutes/example/evaluate", nil)

	depTime, _ := domain.NewDepartureTime("18:00")
	schedule, _ := domain.NewWeeklySchedule([]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}, depTime)
	serviceDate, err := serviceDateFromRequest(request, schedule, now)

	if err != nil {
		t.Fatalf("serviceDateFromRequest() error = %v", err)
	}
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("LoadLocation(Europe/Paris) error = %v", err)
	}
	want := time.Date(2026, time.January, 5, 0, 0, 0, 0, paris)
	if !serviceDate.Equal(want) || serviceDate.Location().String() != paris.String() {
		t.Errorf("serviceDate = %v (%s), want %v (%s)", serviceDate, serviceDate.Location(), want, paris)
	}
}

func TestExplicitServiceDateUsesParisMidnightOnDSTTransition(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/commutes/example/evaluate", strings.NewReader("date=2026-03-29"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	depTime, _ := domain.NewDepartureTime("18:00")
	schedule, _ := domain.NewWeeklySchedule([]time.Weekday{time.Sunday}, depTime)
	serviceDate, err := serviceDateFromRequest(request, schedule, time.Time{})

	if err != nil {
		t.Fatalf("serviceDateFromRequest() error = %v", err)
	}
	if got, want := serviceDate.Format(time.RFC3339), "2026-03-29T00:00:00+01:00"; got != want {
		t.Errorf("serviceDate = %q, want %q", got, want)
	}
}

func TestRateLimitReturns429(t *testing.T) {
	owners := adapters.NewMemoryOwners()
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	server, err := NewServer(fstest.MapFS{}, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(providerStub{}, noopCache{}, deniedLimiter{}),
		CookieName:        "test-owner",
		ProviderAvailable: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	client := newOwnerClient(t, server.Handler())
	response := client.postForm("/commutes", validForm())
	response = client.postForm(response.Header().Get("Location")+"/evaluate", url.Values{})
	if response.Code != http.StatusTooManyRequests {
		t.Errorf("POST evaluate status = %d, want 429", response.Code)
	}
	if !strings.Contains(response.Body.String(), "L’évaluation des itinéraires est limitée.") {
		t.Errorf("rate limit error is not French: %s", response.Body.String())
	}
}

func TestStorageCapacityReturnsFrench503(t *testing.T) {
	t.Run("owners", func(t *testing.T) {
		owners := adapters.NewMemoryOwners(adapters.MemoryRetention{MaxEntries: 1, TTL: time.Hour})
		commutes, err := adapters.NewMemoryCommutes(owners)
		if err != nil {
			t.Fatalf("NewMemoryCommutes() error = %v", err)
		}
		server, err := NewServer(fstest.MapFS{}, Dependencies{Owners: owners, Commutes: commutes, Routes: app.NewRouteService(providerStub{}, noopCache{}, allowLimiter{}), CookieName: "test-owner"})
		if err != nil {
			t.Fatalf("NewServer() error = %v", err)
		}
		if response := newOwnerClient(t, server.Handler()).get("/"); response.Code != http.StatusOK {
			t.Fatalf("first owner GET / status = %d, want 200", response.Code)
		}
		response := newOwnerClient(t, server.Handler()).postForm("/commutes", validForm())
		assertStorageCapacityError(t, response)
	})

	t.Run("commutes", func(t *testing.T) {
		owners := adapters.NewMemoryOwners()
		commutes, err := adapters.NewMemoryCommutes(owners, adapters.MemoryRetention{MaxEntries: 1, TTL: time.Hour})
		if err != nil {
			t.Fatalf("NewMemoryCommutes() error = %v", err)
		}
		server, err := NewServer(fstest.MapFS{}, Dependencies{Owners: owners, Commutes: commutes, Routes: app.NewRouteService(providerStub{}, noopCache{}, allowLimiter{}), CookieName: "test-owner"})
		if err != nil {
			t.Fatalf("NewServer() error = %v", err)
		}
		client := newOwnerClient(t, server.Handler())
		if response := client.postForm("/commutes", validForm()); response.Code != http.StatusSeeOther {
			t.Fatalf("first commute POST status = %d, want 303", response.Code)
		}
		assertStorageCapacityError(t, client.postForm("/commutes", validForm()))
	})
}

func assertStorageCapacityError(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage capacity status = %d, want 503", response.Code)
	}
	if !strings.Contains(response.Body.String(), "La capacité de stockage est temporairement atteinte.") {
		t.Errorf("storage capacity error is not French: %s", response.Body.String())
	}
}

func TestEvaluateReportsDepartureShiftedByTheTravellersOwnWalkingTime(t *testing.T) {
	base := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	owners := adapters.NewMemoryOwners()
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	server, err := NewServer(ui.Assets, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(tightTransferProvider{}, noopCache{}, allowLimiter{}),
		CookieName:        "test-owner",
		ProviderAvailable: true,
		Now:               func() time.Time { return base },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date":                 {"2026-08-03"},
		"time":                 {"18:00"},
		"maximum_initial_walk": {"5"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}

	body := response.Body.String()
	if !strings.Contains(body, "17:58") {
		t.Errorf("departure should be the 18:03 boarding minus the traveller's 5 minutes, i.e. 17:58; body = %q", body)
	}
	if strings.Contains(body, `<div class="result-time-value">18:00</div>`) {
		t.Error("the provider's own walking assumption was reported instead of the traveller's")
	}
}

func TestEvaluateKeepsItineraryWhoseInterchangeIsTighterThanDeclared(t *testing.T) {
	server := newTestServer(t, tightTransferProvider{})
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date":                  {"2026-08-03"},
		"time":                  {"18:00"},
		"selected_combination":  {"Métro 3 + RER A"},
		"saint_lazare_transfer": {"5"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}

	body := response.Body.String()
	if strings.Contains(body, "Aucun itinéraire ne respecte ces temps") {
		t.Error("a 2 minute interchange against a declared 5 minutes discarded the itinerary; it must be kept and reported instead")
	}
	if !strings.Contains(body, "itinéraires admissibles") {
		t.Errorf("expected the itinerary to survive, body = %q", body)
	}
}

func TestStationDisplayNameKeepsAccentsAndDropsTheCityQualifier(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		id           domain.StationID
		want         string
	}{
		{name: "accented name", providerName: "Opéra (Paris)", id: "opra-paris", want: "Opéra"},
		{name: "name repeated as qualifier", providerName: "Cergy-le-Haut (Cergy-le-Haut)", id: "cergy-le-haut-cergy-le-haut", want: "Cergy-le-Haut"},
		{name: "multi word accented", providerName: "Bécon les Bruyères (Courbevoie)", id: "bcon-les-bruyres-courbevoie", want: "Bécon les Bruyères"},
		{name: "no qualifier", providerName: "Villiers", id: "villiers", want: "Villiers"},
		{name: "missing provider name falls back to identifier", providerName: "", id: "gare-du-nord", want: "Gare Du Nord"},
		{name: "only a qualifier", providerName: "(Paris)", id: "paris", want: "(Paris)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stationDisplayName(test.providerName, test.id); got != test.want {
				t.Errorf("stationDisplayName(%q, %q) = %q, want %q", test.providerName, test.id, got, test.want)
			}
		})
	}
}

func TestEvaluateWarnsWhenTheParcoursInterchangeIsTighterThanDeclared(t *testing.T) {
	tests := []struct {
		name            string
		declaredMinutes string
		wantWarning     bool
	}{
		{name: "tighter than declared", declaredMinutes: "5", wantWarning: true},
		{name: "wide enough", declaredMinutes: "1", wantWarning: false},
		{name: "no declared interchange", declaredMinutes: "0", wantWarning: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
			owners := adapters.NewMemoryOwners()
			commutes, err := adapters.NewMemoryCommutes(owners)
			if err != nil {
				t.Fatalf("NewMemoryCommutes() error = %v", err)
			}
			server, err := NewServer(ui.Assets, Dependencies{
				Owners:            owners,
				Commutes:          commutes,
				Routes:            app.NewRouteService(tightTransferProvider{}, noopCache{}, allowLimiter{}),
				CookieName:        "test-owner",
				ProviderAvailable: true,
				Now:               func() time.Time { return base },
			})
			if err != nil {
				t.Fatalf("NewServer() error = %v", err)
			}
			client := newOwnerClient(t, server.Handler())
			form := validForm()
			form.Set("saint_lazare_transfer", "1")
			created := client.postForm("/commutes", form)
			if created.Code != http.StatusSeeOther {
				t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
			}

			response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
				"date":                  {"2026-08-03"},
				"time":                  {"18:00"},
				"selected_combination":  {"Métro 3 + RER A"},
				"saint_lazare_transfer": {test.declaredMinutes},
			})
			if response.Code != http.StatusOK {
				t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
			}

			body := response.Body.String()
			gotWarning := strings.Contains(body, "Correspondance la plus courte")
			if gotWarning != test.wantWarning {
				t.Errorf("interchange warning shown = %v, want %v; a 2 minute interchange must be flagged, never silently accepted", gotWarning, test.wantWarning)
			}
			if test.wantWarning && !strings.Contains(body, "Correspondance la plus courte : 2 min, alors que vous en demandez 5.") {
				t.Errorf("the warning must state the real gap and the declared need; body = %q", body)
			}
		})
	}
}

func TestEvaluateNeverAdvisesAdjustingTimingsThatNoLongerFilter(t *testing.T) {
	server := newTestServer(t, tightTransferProvider{})
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date":                 {"2026-08-03"},
		"time":                 {"18:00"},
		"selected_combination": {"Métro 9 + RER B"},
		"maximum_initial_walk": {"5"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}

	body := response.Body.String()
	if !strings.Contains(body, "Ce parcours n’a pas pu être retrouvé pour cet horaire") {
		t.Errorf("the page must name the parcours as the cause; body = %q", body)
	}
	if strings.Contains(body, "n’est pas proposé") {
		t.Error("the page asserts the parcours does not exist, but a failed provider probe is indistinguishable from a real absence")
	}
	if strings.Contains(body, "Augmentez le temps de marche") || strings.Contains(body, "réduisez le temps de correspondance") {
		t.Error("the page advises adjusting durations that no longer filter anything, which cannot change the outcome")
	}
}

func TestEvaluateExplainsWhenProviderReturnsNoItineraryAtAll(t *testing.T) {
	server := newTestServer(t, providerStub{})
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date": {"2026-08-03"},
		"time": {"18:00"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, "Aucun itinéraire") {
		t.Errorf("an empty result must never render a silently blank page, body = %q", body)
	}
}

var weekdayCheckboxPattern = regexp.MustCompile(`<input[^>]*name="(?:days|weekday)"[^>]*>`)

func assertNoWeekdayPreChecked(t *testing.T, body string) {
	t.Helper()
	for _, checkbox := range weekdayCheckboxPattern.FindAllString(body, -1) {
		if strings.Contains(checkbox, "checked") {
			t.Errorf("weekday checkbox is pre-checked: %s", checkbox)
		}
	}
}

func newTestServer(t *testing.T, provider app.RouteProvider) *Server {
	return newTestServerWithAssets(t, provider, fstest.MapFS{})
}

func newTestServerWithAssets(t *testing.T, provider app.RouteProvider, assets fs.FS) *Server {
	t.Helper()
	owners := adapters.NewMemoryOwners()
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	server, err := NewServer(assets, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(provider, noopCache{}, allowLimiter{}),
		CookieName:        "test-owner",
		ProviderAvailable: false,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func validForm() url.Values {
	return url.Values{
		"name":                  {"Retour à la maison"},
		"origin":                {"Café Rivoli (10 Rue de Rivoli, 75004 Paris)"},
		"destination":           {"Cergy-le-Haut"},
		"departure_time":        {"18:00"},
		"weekday":               {"monday", "tuesday", "wednesday", "thursday", "friday"},
		"maximum_initial_walk":  {"5"},
		"saint_lazare_transfer": {"7"},
	}
}

type ownerClient struct {
	handler http.Handler
	cookie  *http.Cookie
}

func newOwnerClient(t *testing.T, handler http.Handler) *ownerClient {
	t.Helper()
	return &ownerClient{handler: handler}
}

func (c *ownerClient) get(path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	return c.do(request)
}

func (c *ownerClient) postForm(path string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(request)
}

func (c *ownerClient) do(request *http.Request) *httptest.ResponseRecorder {
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "test-owner" {
			c.cookie = cookie
		}
	}
	return response
}

type providerStub struct{}

func (providerStub) Routes(context.Context, app.RouteRequest) ([]domain.Itinerary, error) {
	return nil, nil
}

type tightTransferProvider struct{}

func (tightTransferProvider) Routes(context.Context, app.RouteRequest) ([]domain.Itinerary, error) {
	day := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	at := func(hour, minute int) time.Time {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
	walk, err := domain.NewWalkingLeg(at(18, 0), at(18, 3))
	if err != nil {
		return nil, err
	}
	first, err := domain.NewTransitLegWithLabel("villiers", "chatelet", at(18, 3), at(18, 20), domain.NewTransitLabel("Métro", "3"))
	if err != nil {
		return nil, err
	}
	second, err := domain.NewTransitLegWithLabel("chatelet", "cergy-le-haut", at(18, 22), at(18, 45), domain.NewTransitLabel("RER", "A"))
	if err != nil {
		return nil, err
	}
	itinerary, err := domain.NewItinerary([]domain.ItineraryLeg{walk, first, second})
	if err != nil {
		return nil, err
	}
	return []domain.Itinerary{itinerary}, nil
}

type unavailableProvider struct{}

func (unavailableProvider) Routes(context.Context, app.RouteRequest) ([]domain.Itinerary, error) {
	return nil, app.ErrProviderUnavailable
}

type noopCache struct{}

func (noopCache) Get(string) ([]domain.Itinerary, bool) { return nil, false }
func (noopCache) Set(string, []domain.Itinerary)        {}

type allowLimiter struct{}

func (allowLimiter) Allow() bool { return true }

type deniedLimiter struct{}

func (deniedLimiter) Allow() bool { return false }

type trackingOwnerStore struct {
	app.OwnerStore
	touches int
}

func (s *trackingOwnerStore) Touch(ctx context.Context, ownerID app.OwnerID) (bool, error) {
	s.touches++
	return s.OwnerStore.Touch(ctx, ownerID)
}

func TestPRIMWordingRegression(t *testing.T) {
	server := newTestServerWithAssets(t, unavailableProvider{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())

	response := client.get("/")
	body := response.Body.String()

	if !strings.Contains(body, "ÉVALUATION PRIM INDISPONIBLE") {
		t.Errorf("index page does not contain accurate PRIM wording: %s", body)
	}

	obsoleteWording := []string{
		"RÉSEAU IDFM INDISPONIBLE",
		"Impossible de contacter Île-de-France Mobilités",
		"Le fournisseur d’itinéraires est indisponible.",
	}
	for _, word := range obsoleteWording {
		if strings.Contains(body, word) {
			t.Errorf("index page contains obsolete wording %q: %s", word, body)
		}
	}

	form := validForm()
	response = client.postForm("/commutes", form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", response.Code)
	}
	location := response.Header().Get("Location")

	response = client.postForm(location+"/evaluate", url.Values{})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST evaluate status = %d, want 503", response.Code)
	}
	evalBody := response.Body.String()

	if !strings.Contains(evalBody, "L&#39;évaluation PRIM est indisponible dans cette version.") {
		t.Errorf("evaluation result does not contain accurate PRIM wording: %s", evalBody)
	}

	for _, word := range obsoleteWording {
		if strings.Contains(evalBody, word) {
			t.Errorf("evaluation result contains obsolete wording %q: %s", word, evalBody)
		}
	}
}

func TestFormUXRegression(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())

	t.Run("blank initial form and absent suggestion wording", func(t *testing.T) {
		response := client.get("/")
		if response.Code != http.StatusOK {
			t.Fatalf("GET / status = %d", response.Code)
		}
		body := response.Body.String()

		for _, unexpected := range []string{
			">Café Rivoli (10 Rue de Rivoli, 75004 Paris)</textarea>",
			">Café Rivoli, 10 Rue de Rivoli</textarea>",
			">Cergy-le-Haut</textarea>",
			"value=\"18:00\"",
		} {
			if strings.Contains(body, unexpected) {
				t.Errorf("GET / body unexpectedly contains default value pattern %q", unexpected)
			}
		}
		assertNoWeekdayPreChecked(t, body)
		for _, wording := range []string{"SUGGESTIONS PRÉ-REMPLIES", "suggestions par défaut"} {
			if strings.Contains(body, wording) {
				t.Errorf("GET / body unexpectedly contains suggestion wording %q", wording)
			}
		}
	})

	t.Run("field-specific errors", func(t *testing.T) {
		tests := []struct {
			name          string
			modifyForm    func(url.Values)
			expectedError string
		}{
			{
				name: "invalid name",
				modifyForm: func(f url.Values) {
					f.Set("name", "   ")
				},
				expectedError: "nom du trajet est invalide",
			},
			{
				name: "invalid origin",
				modifyForm: func(f url.Values) {
					f.Set("origin", "   ")
				},
				expectedError: "point de départ est invalide",
			},
			{
				name: "invalid destination",
				modifyForm: func(f url.Values) {
					f.Set("destination", "   ")
				},
				expectedError: "destination est invalide",
			},
			{
				name: "invalid departure time",
				modifyForm: func(f url.Values) {
					f.Set("departure_time", "25:00")
				},
				expectedError: "heure de départ est invalide",
			},
			{
				name: "no weekdays",
				modifyForm: func(f url.Values) {
					f.Del("weekday")
					f.Del("days")
				},
				expectedError: "Sélectionnez au moins un jour actif.",
			},
			{
				name: "blank maximum_initial_walk",
				modifyForm: func(f url.Values) {
					f.Set("maximum_initial_walk", "   ")
				},
				expectedError: "La marche initiale maximale doit être un nombre positif de minutes.",
			},
			{
				name: "zero maximum_initial_walk",
				modifyForm: func(f url.Values) {
					f.Set("maximum_initial_walk", "0")
				},
				expectedError: "La marche initiale maximale doit être un nombre positif de minutes.",
			},
			{
				name: "negative maximum_initial_walk",
				modifyForm: func(f url.Values) {
					f.Set("maximum_initial_walk", "-5")
				},
				expectedError: "La marche initiale maximale doit être un nombre positif de minutes.",
			},
			{
				name: "non-numeric maximum_initial_walk",
				modifyForm: func(f url.Values) {
					f.Set("maximum_initial_walk", "abc")
				},
				expectedError: "La marche initiale maximale doit être un nombre positif de minutes.",
			},
			{
				name: "blank saint_lazare_transfer",
				modifyForm: func(f url.Values) {
					f.Set("saint_lazare_transfer", "   ")
				},
				expectedError: "Le temps de correspondance à Saint-Lazare doit être un nombre positif de minutes.",
			},
			{
				name: "zero saint_lazare_transfer",
				modifyForm: func(f url.Values) {
					f.Set("saint_lazare_transfer", "0")
				},
				expectedError: "Le temps de correspondance à Saint-Lazare doit être un nombre positif de minutes.",
			},
			{
				name: "negative saint_lazare_transfer",
				modifyForm: func(f url.Values) {
					f.Set("saint_lazare_transfer", "-3")
				},
				expectedError: "Le temps de correspondance à Saint-Lazare doit être un nombre positif de minutes.",
			},
			{
				name: "non-numeric saint_lazare_transfer",
				modifyForm: func(f url.Values) {
					f.Set("saint_lazare_transfer", "xyz")
				},
				expectedError: "Le temps de correspondance à Saint-Lazare doit être un nombre positif de minutes.",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				form := validForm()
				tc.modifyForm(form)
				response := client.postForm("/commutes", form)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("POST status = %d, want 400", response.Code)
				}
				body := response.Body.String()
				if !strings.Contains(body, tc.expectedError) {
					t.Errorf("expected error %q not found in body: %s", tc.expectedError, body)
				}
			})
		}
	})

	t.Run("valid save", func(t *testing.T) {
		form := validForm()
		response := client.postForm("/commutes", form)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("POST valid form status = %d, want 303", response.Code)
		}
		location := response.Header().Get("Location")
		if !strings.HasPrefix(location, "/commutes/") {
			t.Errorf("unexpected redirect location: %q", location)
		}
	})
}

func TestNewFormIsFullyBlank(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	body := response.Body.String()

	if strings.Contains(body, `id="name" name="name" value="Retour à la maison"`) || strings.Contains(body, `value="Retour à la maison"`) {
		t.Errorf("GET / body unexpectedly contains pre-filled name: %s", body)
	}
	if strings.Contains(body, `value="18:00"`) {
		t.Errorf("GET / body unexpectedly contains pre-filled departure time: %s", body)
	}
	if strings.Contains(body, ">Café Rivoli, 10 Rue de Rivoli</textarea>") || strings.Contains(body, ">Café Rivoli (10 Rue de Rivoli, 75004 Paris)</textarea>") {
		t.Errorf("GET / body unexpectedly contains pre-filled origin: %s", body)
	}
	if strings.Contains(body, ">Cergy-le-Haut</textarea>") {
		t.Errorf("GET / body unexpectedly contains pre-filled destination: %s", body)
	}
	assertNoWeekdayPreChecked(t, body)
}

func TestGenericTemplateHasNoDefaults(t *testing.T) {
	server := newTestServer(t, providerStub{})
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	body := response.Body.String()

	if strings.Contains(body, `value="Café Rivoli (10 Rue de Rivoli, 75004 Paris)"`) {
		t.Errorf("generic template unexpectedly contains pre-filled origin: %s", body)
	}
	if strings.Contains(body, `value="Cergy-le-Haut"`) {
		t.Errorf("generic template unexpectedly contains pre-filled destination: %s", body)
	}
	if strings.Contains(body, `value="18:00"`) {
		t.Errorf("generic template unexpectedly contains pre-filled departure time: %s", body)
	}
	assertNoWeekdayPreChecked(t, body)
}

func TestSubmittedInvalidValuesArePreserved(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())

	form := url.Values{
		"name":           {"Mon trajet invalide"},
		"origin":         {"Origine invalide"},
		"destination":    {"Destination invalide"},
		"departure_time": {"not-a-time"},
		"days":           {"monday"},
	}

	response := client.postForm("/commutes", form)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid form status = %d, want 400", response.Code)
	}
	body := response.Body.String()

	if !strings.Contains(body, `value="Mon trajet invalide"`) {
		t.Errorf("submitted name was not preserved: %s", body)
	}
	if !strings.Contains(body, ">Origine invalide</textarea>") {
		t.Errorf("submitted origin was not preserved: %s", body)
	}
	if !strings.Contains(body, ">Destination invalide</textarea>") {
		t.Errorf("submitted destination was not preserved: %s", body)
	}
	if !strings.Contains(body, `value="not-a-time"`) {
		t.Errorf("submitted departure time was not preserved: %s", body)
	}
	if !strings.Contains(body, `id="day-monday" name="days" value="monday" checked`) {
		t.Errorf("submitted weekday was not preserved: %s", body)
	}
}

func TestValidationMessageMappingForUpdate(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())

	form := validForm()
	created := client.postForm("/commutes", form)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}
	location := created.Header().Get("Location")

	updateForm := validForm()
	updateForm.Set("departure_time", "25:00")

	response := client.postForm(location, updateForm)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid update status = %d, want 400", response.Code)
	}
	body := response.Body.String()

	if !strings.Contains(body, "heure de départ est invalide") {
		t.Errorf("expected validation error message not found in update response: %s", body)
	}
}

func TestUnknownWeekdayValidation(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())

	form := validForm()
	form.Set("weekday", "not-a-day")

	response := client.postForm("/commutes", form)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid weekday status = %d, want 400", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Sélectionnez au moins un jour actif.") {
		t.Errorf("expected validation error message not found in create response: %s", body)
	}

	form2 := validForm()
	created := client.postForm("/commutes", form2)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", created.Code)
	}
	location := created.Header().Get("Location")

	updateForm := validForm()
	updateForm.Set("weekday", "not-a-day")

	response2 := client.postForm(location, updateForm)
	if response2.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid weekday update status = %d, want 400", response2.Code)
	}
	body2 := response2.Body.String()
	if !strings.Contains(body2, "Sélectionnez au moins un jour actif.") {
		t.Errorf("expected validation error message not found in update response: %s", body2)
	}
}

type customRouteProvider struct {
	routes []domain.Itinerary
}

func (p customRouteProvider) Routes(context.Context, app.RouteRequest) ([]domain.Itinerary, error) {
	return p.routes, nil
}

func TestTransitLabelsRendering(t *testing.T) {
	base := time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)

	walkLeg, err := domain.NewWalkingLeg(base, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("NewWalkingLeg() error = %v", err)
	}

	from1, _ := domain.NewStationID("villiers")
	to1, _ := domain.NewStationID("republique")
	label1 := domain.NewTransitLabel("Métro", "3")
	transitLeg1, err := domain.NewTransitLegWithLabel(from1, to1, base.Add(5*time.Minute), base.Add(15*time.Minute), label1)
	if err != nil {
		t.Fatalf("NewTransitLegWithLabel() error = %v", err)
	}

	from2, _ := domain.NewStationID("chatelet-les-halles")
	to2, _ := domain.NewStationID("cergy-le-haut")
	label2 := domain.NewTransitLabel("RER", "A")
	transitLeg2, err := domain.NewTransitLegWithLabel(from2, to2, base.Add(20*time.Minute), base.Add(35*time.Minute), label2)
	if err != nil {
		t.Fatalf("NewTransitLegWithLabel() error = %v", err)
	}

	from3, _ := domain.NewStationID("saint-lazare")
	to3, _ := domain.NewStationID("pont-cardinet")
	transitLeg3, err := domain.NewTransitLeg(from3, to3, base.Add(40*time.Minute), base.Add(45*time.Minute))
	if err != nil {
		t.Fatalf("NewTransitLeg() error = %v", err)
	}

	itinerary, err := domain.NewItinerary([]domain.ItineraryLeg{walkLeg, transitLeg1, transitLeg2, transitLeg3})
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}

	provider := customRouteProvider{routes: []domain.Itinerary{itinerary}}
	owners := adapters.NewMemoryOwners()
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}

	server, err := NewServer(ui.Assets, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(provider, noopCache{}, allowLimiter{}),
		CookieName:        "test-owner",
		ProviderAvailable: true,
		Now:               func() time.Time { return base },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	client := newOwnerClient(t, server.Handler())
	response := client.postForm("/commutes", validForm())
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want 303", response.Code)
	}
	location := response.Header().Get("Location")

	evalResponse := client.postForm(location+"/evaluate", url.Values{
		"date":                 {"2026-08-03"},
		"selected_combination": {"Métro 3 + RER A"},
	})
	if evalResponse.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want 200", evalResponse.Code)
	}

	body := evalResponse.Body.String()

	if !strings.Contains(body, "Métro 3") {
		t.Errorf("expected 'Métro 3' in rendered output, but not found: %s", body)
	}
	if !strings.Contains(body, "RER A") {
		t.Errorf("expected 'RER A' in rendered output, but not found: %s", body)
	}

	if !strings.Contains(body, "Marche initiale") {
		t.Errorf("expected 'Marche initiale' in rendered output, but not found: %s", body)
	}

	if !strings.Contains(body, "Saint Lazare") || !strings.Contains(body, "Pont Cardinet") {
		t.Errorf("expected 'Saint Lazare' and 'Pont Cardinet' in rendered output, but not found: %s", body)
	}

	if strings.Contains(body, "· Saint Lazare") || strings.Contains(body, "· <strong>Saint Lazare</strong>") {
		t.Errorf("unexpected middle dot before label-free transit station: %s", body)
	}
}

func TestServiceWorkerCacheControl(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sw.js", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET /sw.js status = %d, want 200", response.Code)
	}
	if cacheControl := response.Result().Header.Get("Cache-Control"); cacheControl != "no-cache" {
		t.Errorf("GET /sw.js Cache-Control = %q, want no-cache", cacheControl)
	}
}

func TestEverySavedCommutePageKeepsTheCommuteListSelectable(t *testing.T) {
	server := newTestServerWithAssets(t, providerStub{}, ui.Assets)
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	for _, page := range []struct {
		name     string
		response *httptest.ResponseRecorder
	}{
		{name: "home", response: client.get("/")},
		{name: "saved commute", response: client.get(location)},
		{name: "evaluation", response: client.postForm(location+"/evaluate", url.Values{})},
	} {
		t.Run(page.name, func(t *testing.T) {
			body := page.response.Body.String()
			if !strings.Contains(body, "Retour à la maison") {
				t.Errorf("page does not list the saved commute, so the segmented control has nothing to show")
			}
			if disabledTabPattern.MatchString(body) {
				t.Errorf("the saved-commutes segment is disabled although a commute exists, making the control unusable")
			}
		})
	}
}

var disabledTabPattern = regexp.MustCompile(`<input[^>]*id="tab-commutes"[^>]*\sdisabled`)

func TestEmbeddedAssetsKeepCacheVersionsInSync(t *testing.T) {
	swVersion := singleAssetVersion(t, "static/sw.js", regexp.MustCompile(`const CACHE_NAME = "mind-the-time-v(\d+)";`))
	scriptVersion := singleAssetVersion(t, "templates/index.html", regexp.MustCompile(`<script src="/static/app\.js\?v=(\d+)"></script>`))
	styleVersion := singleAssetVersion(t, "templates/index.html", regexp.MustCompile(`<link rel="stylesheet" href="/static/style\.css\?v=(\d+)">`))

	if swVersion != scriptVersion || swVersion != styleVersion {
		t.Errorf("asset versions are out of sync: sw.js cache = %s, app.js = %s, style.css = %s; bumping one without the others serves stale assets",
			swVersion, scriptVersion, styleVersion)
	}
}

// Requiring exactly one match stops a stale version left in a comment from satisfying the check.
func singleAssetVersion(t *testing.T, assetPath string, pattern *regexp.Regexp) string {
	t.Helper()
	content, err := fs.ReadFile(ui.Assets, assetPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", assetPath, err)
	}
	matches := pattern.FindAllStringSubmatch(string(content), -1)
	if len(matches) != 1 {
		t.Fatalf("%s matches %q %d time(s), want exactly 1", assetPath, pattern, len(matches))
	}
	return matches[0][1]
}

func TestEmbeddedAssetsKeepAddressFieldsLocal(t *testing.T) {
	templateBytes, err := fs.ReadFile(ui.Assets, "templates/index.html")
	if err != nil {
		t.Fatalf("failed to read templates/index.html: %v", err)
	}
	templateContent := string(templateBytes)
	for _, field := range []string{
		`<textarea id="origin" name="origin"`,
		`<textarea id="destination" name="destination"`,
	} {
		if !strings.Contains(templateContent, field) {
			t.Errorf("index.html does not contain plain local address field %q: %s", field, templateContent)
		}
	}

	appBytes, err := fs.ReadFile(ui.Assets, "static/app.js")
	if err != nil {
		t.Fatalf("failed to read static/app.js: %v", err)
	}
	appContent := string(appBytes)
	for _, forbidden := range []string{
		"api-adresse.data.gouv.fr",
		"initAutocomplete",
		"setupAutocomplete",
	} {
		if strings.Contains(appContent, forbidden) {
			t.Errorf("app.js contains forbidden address autocomplete setup %q: %s", forbidden, appContent)
		}
	}
}

func TestPunctualDateAndTimeEvaluation(t *testing.T) {
	base := time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)
	from, _ := domain.NewStationID("villiers")
	to, _ := domain.NewStationID("republique")
	transitLeg, _ := domain.NewTransitLeg(from, to, base.Add(30*time.Minute), base.Add(45*time.Minute))
	itinerary, _ := domain.NewItinerary([]domain.ItineraryLeg{transitLeg})

	provider := customRouteProvider{routes: []domain.Itinerary{itinerary}}
	owners := adapters.NewMemoryOwners()
	commutes, _ := adapters.NewMemoryCommutes(owners)
	server, err := NewServer(ui.Assets, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(provider, noopCache{}, allowLimiter{}),
		CookieName:        "test-owner",
		ProviderAvailable: true,
		Now:               func() time.Time { return base },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	location := created.Header().Get("Location")

	evalResponse := client.postForm(location+"/evaluate", url.Values{"date": {"2026-08-03"}, "time": {"18:30"}})
	if evalResponse.Code != http.StatusOK {
		t.Fatalf("POST evaluate with custom time status = %d, want 200", evalResponse.Code)
	}

	body := evalResponse.Body.String()
	if !strings.Contains(body, `value="18:30"`) {
		t.Errorf("eval response expected custom eval time 18:30 in input, got: %s", body)
	}
}

type plannerStub struct {
	calls    int
	pattern  domain.RoutePattern
	timings  domain.PersonalTimings
	earliest time.Time
	window   time.Duration
	options  []domain.DepartureOption
	failure  error
}

func (p *plannerStub) Plan(_ context.Context, request app.PlanRequest) ([]domain.DepartureOption, error) {
	p.calls++
	p.pattern = request.Pattern
	p.timings = request.Timings
	p.earliest = request.Earliest
	p.window = request.Window
	return p.options, p.failure
}

func identifiedItinerary(t *testing.T) domain.Itinerary {
	t.Helper()
	first, err := domain.NewTransitLegWithProviderIdentity("villiers", "saint-lazare", "Villiers", "Saint-Lazare",
		time.Date(2026, time.August, 3, 18, 3, 0, 0, time.UTC), time.Date(2026, time.August, 3, 18, 20, 0, 0, time.UTC),
		domain.NewTransitLabel("Métro", "3"), "line:IDFM:C01373", "stop_point:villiers", "stop_point:saint-lazare")
	if err != nil {
		t.Fatalf("NewTransitLegWithProviderIdentity() error = %v", err)
	}
	second, err := domain.NewTransitLegWithProviderIdentity("saint-lazare", "cergy-le-haut", "Gare Saint-Lazare", "Cergy-le-Haut",
		time.Date(2026, time.August, 3, 18, 25, 0, 0, time.UTC), time.Date(2026, time.August, 3, 18, 55, 0, 0, time.UTC),
		domain.NewTransitLabel("Train Transilien", "L"), "line:IDFM:C01740", "stop_point:saint-lazare", "stop_point:cergy")
	if err != nil {
		t.Fatalf("NewTransitLegWithProviderIdentity() error = %v", err)
	}
	itinerary, err := domain.NewItinerary([]domain.ItineraryLeg{first, second})
	if err != nil {
		t.Fatalf("NewItinerary() error = %v", err)
	}
	return itinerary
}

func serverWithPlanner(t *testing.T, planner DeparturePlanner, itinerary domain.Itinerary) *Server {
	t.Helper()
	owners := adapters.NewMemoryOwners()
	commutes, err := adapters.NewMemoryCommutes(owners)
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	server, err := NewServer(ui.Assets, Dependencies{
		Owners:            owners,
		Commutes:          commutes,
		Routes:            app.NewRouteService(customRouteProvider{routes: []domain.Itinerary{itinerary}}, noopCache{}, allowLimiter{}),
		Planner:           planner,
		CookieName:        "test-owner",
		ProviderAvailable: true,
		Now:               func() time.Time { return time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func evaluateSelecting(t *testing.T, server *Server, combination string) string {
	t.Helper()
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date":                  {"2026-08-03"},
		"time":                  {"18:00"},
		"selected_combination":  {combination},
		"maximum_initial_walk":  {"5"},
		"saint_lazare_transfer": {"7"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}
	return response.Body.String()
}

func TestEvaluatePlansDeparturesForTheSelectedParcoursUsingProviderIdentifiers(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))

	evaluateSelecting(t, server, "Métro 3 + Train Transilien L")

	if planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", planner.calls)
	}
	legs := planner.pattern.Legs()
	if len(legs) != 2 {
		t.Fatalf("pattern legs = %d, want 2", len(legs))
	}
	if got, want := legs[1].Line(), domain.ProviderLineID("line:IDFM:C01740"); got != want {
		t.Errorf("planned on line %q, want %q; planning on a display label cannot tell two branches apart", got, want)
	}
	if got, want := planner.timings.AccessToFirstBoard().Duration(), 5*time.Minute; got != want {
		t.Errorf("access time = %v, want %v (the traveller's own value)", got, want)
	}
	if got, want := planner.timings.Interchange().Duration(), 7*time.Minute; got != want {
		t.Errorf("interchange = %v, want %v (the traveller's own value)", got, want)
	}
}

func TestEvaluateWithoutAPlannerKeepsThePreviousBehaviour(t *testing.T) {
	server := serverWithPlanner(t, nil, identifiedItinerary(t))

	body := evaluateSelecting(t, server, "Métro 3 + Train Transilien L")

	if !strings.Contains(body, "chosen-parcours-card") {
		t.Error("the page stopped rendering the chosen parcours when no planner is configured")
	}
}

func TestEvaluateSurvivesAPlannerFailureWithoutBreakingThePage(t *testing.T) {
	planner := &plannerStub{failure: errors.New("timetable unavailable")}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))

	body := evaluateSelecting(t, server, "Métro 3 + Train Transilien L")

	if !strings.Contains(body, "chosen-parcours-card") {
		t.Error("a planner failure took the whole page down instead of degrading quietly")
	}
}

func TestPlanDeparturesDiscardsOptionsWhenThePlannerAlsoReportsAFailure(t *testing.T) {
	leaveAt := time.Date(2026, time.August, 3, 18, 8, 0, 0, time.UTC)
	arrival := time.Date(2026, time.August, 3, 19, 3, 0, 0, time.UTC)
	connection, err := domain.NewObservedConnection("vj-1", domain.NewTransitLabel("Train Transilien", "L"),
		"saint-lazare", "cergy-le-haut", "Gare Saint-Lazare", "Cergy-le-Haut", leaveAt, arrival)
	if err != nil {
		t.Fatalf("NewObservedConnection() error = %v", err)
	}
	snapshot, err := domain.NewTimetableSnapshot([][]domain.ObservedConnection{{connection}})
	if err != nil {
		t.Fatalf("NewTimetableSnapshot() error = %v", err)
	}
	window, err := domain.NewDepartureWindow(leaveAt.Add(-time.Hour), 2*time.Hour)
	if err != nil {
		t.Fatalf("NewDepartureWindow() error = %v", err)
	}
	zero, err := domain.NewTransferBuffer(0)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	stale := domain.PlanDepartureOptions(snapshot, domain.NewPersonalTimings(zero, zero), window)
	if len(stale) == 0 {
		t.Fatal("fixture produced no option to discard")
	}

	planner := &plannerStub{options: stale, failure: errors.New("timetable unavailable")}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))

	start := time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)
	displays := server.planDepartures(context.Background(), []domain.Itinerary{identifiedItinerary(t)},
		commuteForPlanning(t), searchWindow{start: start, end: start.Add(30 * time.Minute)})

	if displays != nil {
		t.Errorf("displays = %v, want nil; options returned alongside an error are not trustworthy", displays)
	}
}

func commuteForPlanning(t *testing.T) domain.Commute {
	t.Helper()
	_, commute, err := app.NewCommute(app.CommuteInput{
		Name:                       "Retour",
		Origin:                     "10 Rue de Rivoli, 75004 Paris",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "7",
		Weekdays:                   []time.Weekday{time.Monday},
	})
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	return commute
}

func TestDepartureOptionDisplaysAreArithmeticallyConsistent(t *testing.T) {
	// Provider timetable cells carry seconds, so a raw duration disagrees with
	// the clock values a reader sees.
	board := time.Date(2026, time.August, 12, 18, 28, 40, 0, time.UTC)
	alight := time.Date(2026, time.August, 12, 19, 13, 10, 0, time.UTC)
	connection, err := domain.NewObservedConnection("vj-1", domain.NewTransitLabel("Train Transilien", "L"),
		"saint-lazare", "cergy-le-haut", "Gare Saint-Lazare", "Cergy-le-Haut", board, alight)
	if err != nil {
		t.Fatalf("NewObservedConnection() error = %v", err)
	}
	snapshot, err := domain.NewTimetableSnapshot([][]domain.ObservedConnection{{connection}})
	if err != nil {
		t.Fatalf("NewTimetableSnapshot() error = %v", err)
	}
	zero, err := domain.NewTransferBuffer(0)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	window, err := domain.NewDepartureWindow(board.Add(-time.Hour), 2*time.Hour)
	if err != nil {
		t.Fatalf("NewDepartureWindow() error = %v", err)
	}

	displays := departureOptionDisplays(domain.PlanDepartureOptions(snapshot, domain.NewPersonalTimings(zero, zero), window))
	if len(displays) != 1 {
		t.Fatalf("displays = %d, want 1", len(displays))
	}

	shown := displays[0]
	leaveAt, err := time.Parse("15:04", shown.LeaveAt)
	if err != nil {
		t.Fatalf("time.Parse(LeaveAt) error = %v", err)
	}
	arrival, err := time.Parse("15:04", shown.Arrival)
	if err != nil {
		t.Fatalf("time.Parse(Arrival) error = %v", err)
	}
	want := fmt.Sprintf("%d min", int(arrival.Sub(leaveAt).Minutes()))
	if shown.Duration != want {
		t.Errorf("Duration = %q for %s -> %s, want %q; the reader cannot check the arithmetic",
			shown.Duration, shown.LeaveAt, shown.Arrival, want)
	}
}

func TestResolveSearchWindowHonoursTheRequestedRange(t *testing.T) {
	serviceDate := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	departure := time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name      string
		form      url.Values
		wantStart string
		wantEnd   string
	}{
		{name: "no range falls back to the evaluation time", form: url.Values{}, wantStart: "18:00", wantEnd: "18:30"},
		{name: "range starting before the evaluation time", form: url.Values{"window_start": {"17:40"}, "window_end": {"18:30"}}, wantStart: "17:40", wantEnd: "18:30"},
		{name: "only a start keeps the default width", form: url.Values{"window_start": {"17:00"}}, wantStart: "17:00", wantEnd: "18:30"},
		{name: "inverted range is repaired", form: url.Values{"window_start": {"18:30"}, "window_end": {"18:00"}}, wantStart: "18:30", wantEnd: "19:00"},
		{name: "invalid input is ignored", form: url.Values{"window_start": {"pas une heure"}}, wantStart: "18:00", wantEnd: "18:30"},
	} {
		t.Run(test.name, func(t *testing.T) {
			window := resolveSearchWindow(test.form, serviceDate, departure, time.UTC)
			if got := window.start.Format("15:04"); got != test.wantStart {
				t.Errorf("start = %s, want %s", got, test.wantStart)
			}
			if got := window.end.Format("15:04"); got != test.wantEnd {
				t.Errorf("end = %s, want %s", got, test.wantEnd)
			}
		})
	}
}

func TestEvaluatePlansOverTheRangeTheTravellerAskedFor(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/evaluate", url.Values{
		"date":                 {"2026-08-03"},
		"time":                 {"18:00"},
		"selected_combination": {"Métro 3 + Train Transilien L"},
		"window_start":         {"17:40"},
		"window_end":           {"18:30"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("POST evaluate status = %d, want %d", response.Code, http.StatusOK)
	}
	if planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", planner.calls)
	}
	if got, want := planner.earliest.Format("15:04"), "17:40"; got != want {
		t.Errorf("planned from %s, want %s; a departure before the evaluation time must stay reachable", got, want)
	}
	if got, want := planner.window, 50*time.Minute; got != want {
		t.Errorf("planned window = %v, want %v", got, want)
	}
}

func TestEvaluateShowsASingleSourceOfTruthForTheDepartureTime(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))

	body := evaluateSelecting(t, server, "Métro 3 + Train Transilien L")

	for _, legacy := range []string{"Départ recommandé", "Alternative de secours"} {
		if strings.Contains(body, legacy) {
			t.Errorf("the page still renders %q, which is computed from the old path and contradicts the planner", legacy)
		}
	}
	if !strings.Contains(body, "Votre heure de départ") {
		t.Error("step 3 lost its heading")
	}
}

func TestDefaultExploration(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	response := client.postForm(location+"/explore", url.Values{})
	if response.Code != http.StatusOK {
		t.Fatalf("POST explore status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Explorer les combinaisons") {
		t.Errorf("expected 'Explorer les combinaisons' in body: %s", body)
	}
	if !strings.Contains(body, "Métro 3") {
		t.Errorf("expected discovered candidate 'Métro 3' in body: %s", body)
	}
}

func TestCustomWindowValidation(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	response := client.postForm(location+"/explore", url.Values{
		"window_start": {"18:00"},
		"window_end":   {"17:00"},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST explore with invalid range status = %d, want 400", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "La plage de recherche doit être croissante") {
		t.Errorf("expected 'La plage de recherche doit être croissante' in body: %s", body)
	}
}

func TestClientFingerprintVerificationByRerunningDiscovery(t *testing.T) {
	planner := &plannerStub{}
	itinerary := identifiedItinerary(t)
	server := serverWithPlanner(t, planner, itinerary)
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	pattern, err := itinerary.RoutePattern()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := pattern.Fingerprint()

	response := client.postForm(location+"/save-combinations", url.Values{
		"fingerprints": {fingerprint},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST save-combinations status = %d, want 303", response.Code)
	}

	commutePage := client.get(location)
	if !strings.Contains(commutePage.Body.String(), "Métro 3") || !strings.Contains(commutePage.Body.String(), "Train Transilien L") {
		t.Errorf("expected saved combination to be listed: %s", commutePage.Body.String())
	}

	responseZero := client.postForm(location+"/save-combinations", url.Values{})
	if responseZero.Code != http.StatusBadRequest {
		t.Fatalf("POST save-combinations with zero selection status = %d, want 400", responseZero.Code)
	}
	if !strings.Contains(responseZero.Body.String(), "Veuillez sélectionner au moins un parcours à enregistrer.") {
		t.Errorf("expected zero selection error message in body: %s", responseZero.Body.String())
	}

	commutePageAfterZero := client.get(location)
	if !strings.Contains(commutePageAfterZero.Body.String(), "Métro 3") || !strings.Contains(commutePageAfterZero.Body.String(), "Train Transilien L") {
		t.Errorf("expected original saved combination to remain listed after zero selection: %s", commutePageAfterZero.Body.String())
	}

	responseForged := client.postForm(location+"/save-combinations", url.Values{
		"fingerprints": {"fabricated-fingerprint"},
	})
	if responseForged.Code != http.StatusBadRequest {
		t.Fatalf("POST save-combinations with forged fingerprint status = %d, want 400", responseForged.Code)
	}
	if !strings.Contains(responseForged.Body.String(), "Certains parcours sélectionnés sont invalides ou obsolètes.") {
		t.Errorf("expected forged fingerprint error message in body: %s", responseForged.Body.String())
	}

	commutePageAfterForged := client.get(location)
	if !strings.Contains(commutePageAfterForged.Body.String(), "Métro 3") || !strings.Contains(commutePageAfterForged.Body.String(), "Train Transilien L") {
		t.Errorf("expected original saved combination to remain listed after forged fingerprint: %s", commutePageAfterForged.Body.String())
	}
}

func TestMultipleSavedCombinationsWithOpaqueIDs(t *testing.T) {
	planner := &plannerStub{}
	itinerary := identifiedItinerary(t)
	server := serverWithPlanner(t, planner, itinerary)
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	pattern, err := itinerary.RoutePattern()
	if err != nil {
		t.Fatal(err)
	}
	response := client.postForm(location+"/save-combinations", url.Values{
		"fingerprints": {pattern.Fingerprint()},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST save-combinations status = %d, want 303", response.Code)
	}

	commutePage := client.get(location)
	body := commutePage.Body.String()
	if !strings.Contains(body, "Planifier un départ") {
		t.Errorf("expected 'Planifier un départ' section in body: %s", body)
	}
}

func TestPlanWithForgedSavedCombinationIDReturnsBadRequest(t *testing.T) {
	planner := &plannerStub{}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}

	response := client.postForm(created.Header().Get("Location")+"/plan", url.Values{
		"saved_combination_ids": {"forged-saved-combination-id"},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST plan with forged saved combination ID status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "Certains parcours sélectionnés sont invalides ou obsolètes.") {
		t.Errorf("expected forged saved combination ID error message in body: %s", response.Body.String())
	}
}

func TestSubsetRuntimePlanningAndDistinctOutcomes(t *testing.T) {
	leaveAt := time.Date(2026, time.August, 3, 18, 8, 0, 0, time.UTC)
	arrival := time.Date(2026, time.August, 3, 19, 3, 0, 0, time.UTC)
	connection, err := domain.NewObservedConnection("vj-1", domain.NewTransitLabel("Train Transilien", "L"),
		"saint-lazare", "cergy-le-haut", "Gare Saint-Lazare", "Cergy-le-Haut", leaveAt, arrival)
	if err != nil {
		t.Fatalf("NewObservedConnection() error = %v", err)
	}
	snapshot, err := domain.NewTimetableSnapshot([][]domain.ObservedConnection{{connection}})
	if err != nil {
		t.Fatalf("NewTimetableSnapshot() error = %v", err)
	}
	zero, err := domain.NewTransferBuffer(0)
	if err != nil {
		t.Fatalf("NewTransferBuffer() error = %v", err)
	}
	window, err := domain.NewDepartureWindow(leaveAt.Add(-time.Hour), 2*time.Hour)
	if err != nil {
		t.Fatalf("NewDepartureWindow() error = %v", err)
	}
	options := domain.PlanDepartureOptions(snapshot, domain.NewPersonalTimings(zero, zero), window)

	planner := &plannerStub{options: options}
	server := serverWithPlanner(t, planner, identifiedItinerary(t))
	client := newOwnerClient(t, server.Handler())
	created := client.postForm("/commutes", validForm())
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /commutes status = %d, want %d", created.Code, http.StatusSeeOther)
	}
	location := created.Header().Get("Location")

	itinerary := identifiedItinerary(t)
	pattern, _ := itinerary.RoutePattern()
	client.postForm(location+"/save-combinations", url.Values{
		"fingerprints": {pattern.Fingerprint()},
	})

	commutePage := client.get(location)
	body := commutePage.Body.String()

	re := regexp.MustCompile(`name="saved_combination_ids" value="([^"]+)"`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 2 {
		t.Fatalf("could not find saved combination ID in body: %s", body)
	}
	savedID := matches[1]

	planResponse := client.postForm(location+"/plan", url.Values{
		"saved_combination_ids": {savedID},
	})
	if planResponse.Code != http.StatusOK {
		t.Fatalf("POST plan status = %d, want 200", planResponse.Code)
	}
	planBody := planResponse.Body.String()
	if !strings.Contains(planBody, "Départs viables trouvés") {
		t.Errorf("expected 'Départs viables trouvés' in body: %s", planBody)
	}

	planner.failure = app.ErrNoTimetableCoverage
	planResponse2 := client.postForm(location+"/plan", url.Values{
		"saved_combination_ids": {savedID},
	})
	if planResponse2.Code != http.StatusOK {
		t.Fatalf("POST plan status = %d, want 200", planResponse2.Code)
	}
	planBody2 := planResponse2.Body.String()
	if !strings.Contains(planBody2, "Pas de couverture horaire pour ce parcours.") {
		t.Errorf("expected 'Pas de couverture horaire pour ce parcours.' in body: %s", planBody2)
	}

	planner.failure = app.ErrProviderUnavailable
	planResponse3 := client.postForm(location+"/plan", url.Values{
		"saved_combination_ids": {savedID},
	})
	if planResponse3.Code != http.StatusOK {
		t.Fatalf("POST plan status = %d, want 200", planResponse3.Code)
	}
	planBody3 := planResponse3.Body.String()
	if !strings.Contains(planBody3, "Le fournisseur d") || !strings.Contains(planBody3, "indisponible") {
		t.Errorf("expected 'Le fournisseur d'horaires est indisponible.' in body: %s", planBody3)
	}
}
