package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

func TestPRIMClientWithoutConfigurationDoesNotMakeNetworkCall(t *testing.T) {
	transport := &countingTransport{}
	client := NewPRIMClient("", "", &http.Client{Transport: transport})

	_, err := client.Routes(context.Background(), routeRequest(t))
	if !errors.Is(err, app.ErrProviderUnavailable) {
		t.Fatalf("Routes() error = %v, want ErrProviderUnavailable", err)
	}
	if transport.calls != 0 {
		t.Errorf("network calls = %d, want 0", transport.calls)
	}
}

func TestPRIMClientUsesApikeyAuthorizationWithoutLeakingSecret(t *testing.T) {
	const secret = "a-secret-api-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Header.Get("apiKey"), secret; got != want {
			t.Errorf("apiKey header = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"journeys":[]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL, secret, server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}
	if len(routes) != 0 {
		t.Errorf("len(routes) = %d, want 0", len(routes))
	}
}

func TestPRIMClientRejectsHTTPWithoutMakingNetworkCallOrLeakingSecret(t *testing.T) {
	const secret = "a-secret-api-key"
	transport := &countingTransport{}
	client := NewPRIMClient("http://prim.example.test/routes", secret, &http.Client{Transport: transport})

	_, err := client.Routes(context.Background(), routeRequest(t))
	if !errors.Is(err, app.ErrProviderUnavailable) {
		t.Fatalf("Routes() error = %v, want ErrProviderUnavailable", err)
	}
	if transport.calls != 0 {
		t.Errorf("network calls = %d, want 0", transport.calls)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Routes() error leaked API key: %v", err)
	}
}

func TestPRIMClientBoundsDiscardedUnsupportedResponseBody(t *testing.T) {
	body := &oversizedBody{remaining: maxResponseBodyBytes + 10}
	transport := &responseTransport{body: body}
	client := NewPRIMClient("https://prim.example.test/routes", "api-key", &http.Client{Transport: transport})

	_, err := client.Routes(context.Background(), routeRequest(t))
	if !errors.Is(err, app.ErrProviderUnsupportedContract) {
		t.Fatalf("Routes() error = %v, want ErrProviderUnsupportedContract", err)
	}
	if !body.closed {
		t.Error("response body was not closed")
	}
}

func TestPRIMClientNeverFabricatesStationsOrLinesNotReturnedByProvider(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{
			"journeys": [{
				"departure_date_time": "20260803T180000",
				"arrival_date_time": "20260803T182000",
				"sections": [
					{"type":"street_network","mode":"walking","departure_date_time":"20260803T180000","arrival_date_time":"20260803T180300"},
					{"type":"public_transport","departure_date_time":"20260803T180300","arrival_date_time":"20260803T182000","from":{"name":"Montparnasse"},"to":{"name":"La Défense"},"display_informations":{"physical_mode":"Métro","code":"13"}}
				]
			}]
		}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL, "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}

	for _, route := range routes {
		for _, leg := range route.Legs() {
			if leg.Mode() != "transit" {
				continue
			}
			from, to := string(leg.From()), string(leg.To())
			if strings.Contains(from, "villiers") || strings.Contains(to, "villiers") {
				t.Errorf("provider never returned Villiers but itinerary contains it: %s -> %s", from, to)
			}
			label, ok := leg.TransitLabel()
			if ok && label.Code() == "3" {
				t.Errorf("provider never returned line 3 but itinerary contains it: %s %s", label.Network(), label.Code())
			}
		}
	}
}

func TestPRIMClientRoutesMapsSectionsInOrderAndPreservesTransitLabels(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"journeys": [{
				"departure_date_time": "20260803T180000",
				"arrival_date_time": "20260803T184500",
				"sections": [
					{"type":"street_network","mode":"walking","departure_date_time":"20260803T180000","arrival_date_time":"20260803T180500"},
					{"type":"public_transport","mode":"walking","departure_date_time":"20260803T180500","arrival_date_time":"20260803T181500","from":{"name":"Villiers"},"to":{"name":"République"},"display_informations":{"physical_mode":" Métro ","network":"Bus","code":" 3 "}},
					{"type":"crow_fly","mode":"walking","departure_date_time":"20260803T181500","arrival_date_time":"20260803T181600"},
					{"type":"walking","mode":"walking","departure_date_time":"20260803T181500","arrival_date_time":"20260803T182000"},
					{"type":"transfer","mode":"bicycle","transfer_type":"bicycle","departure_date_time":"20260803T182000","arrival_date_time":"20260803T182100"},
					{"type":"transfer","mode":"walking","transfer_type":"walking","departure_date_time":"20260803T182000","arrival_date_time":"20260803T182300"},
					{"type":"public_transport","departure_date_time":"20260803T182300","arrival_date_time":"20260803T184500","from":{"name":"Châtelet"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":" RER ","code":" A "}}
				]
			}]
		}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL, "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}
	if got, want := len(routes), 1; got != want {
		t.Fatalf("len(routes) = %d, want %d", got, want)
	}

	legs := routes[0].Legs()
	if got, want := len(legs), 5; got != want {
		t.Fatalf("len(legs) = %d, want %d", got, want)
	}
	for index, wantMode := range []string{"walking", "transit", "walking", "walking", "transit"} {
		if got := legs[index].Mode(); got != wantMode {
			t.Errorf("legs[%d].Mode() = %q, want %q", index, got, wantMode)
		}
	}

	label, ok := legs[1].TransitLabel()
	if !ok {
		t.Fatal("legs[1].TransitLabel() present = false, want true")
	}
	if got, want := label.Network(), "Métro"; got != want {
		t.Errorf("legs[1] label network = %q, want %q", got, want)
	}
	if got, want := label.Code(), "3"; got != want {
		t.Errorf("legs[1] label code = %q, want %q", got, want)
	}
	if _, ok := legs[0].TransitLabel(); ok {
		t.Error("walking leg has a transit label")
	}
	if _, ok := legs[3].TransitLabel(); ok {
		t.Error("walking transfer has a transit label")
	}
	if got, want := legs[3].Duration(), 3*time.Minute; got != want {
		t.Errorf("walking transfer duration = %v, want %v", got, want)
	}
	label, ok = legs[4].TransitLabel()
	if !ok {
		t.Fatal("legs[4].TransitLabel() present = false, want true")
	}
	if got, want := label.Network(), "RER"; got != want {
		t.Errorf("legs[4] label network = %q, want %q", got, want)
	}
	if got, want := label.Code(), "A"; got != want {
		t.Errorf("legs[4] label code = %q, want %q", got, want)
	}
	if got, want := legs[1].From(), domain.StationID("villiers"); got != want {
		t.Errorf("legs[1].From() = %q, want %q", got, want)
	}
}

func TestPRIMClientResolvesStreetAddressToItsOwnCoordinatesNotANearbyStation(t *testing.T) {
	const addressCoordinates = "2.352245;48.856188"
	var mu sync.Mutex
	var gotFrom, gotTo string

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			query := request.URL.Query().Get("q")
			switch {
			case strings.Contains(query, "Rivoli"):
				_, _ = writer.Write([]byte(`{"places":[{"id":"` + addressCoordinates + `","embedded_type":"address","name":"10 Rue de Rivoli"}]}`))
			case strings.Contains(query, "Cergy-le-Haut"):
				_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:70001","embedded_type":"stop_area","name":"Cergy-le-Haut"}]}`))
			default:
				_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:71889","embedded_type":"stop_area","name":"Porte des Lilas"}]}`))
			}
			return
		}
		mu.Lock()
		if gotFrom == "" {
			gotFrom, gotTo = request.URL.Query().Get("from"), request.URL.Query().Get("to")
		}
		mu.Unlock()
		_, _ = writer.Write([]byte(`{"journeys":[]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	if _, err := client.Routes(context.Background(), routeRequest(t)); err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotFrom != addressCoordinates {
		t.Errorf("origin sent to provider = %q, want the address coordinates %q", gotFrom, addressCoordinates)
	}
	if gotTo != "stop_area:IDFM:70001" {
		t.Errorf("destination sent to provider = %q, want %q", gotTo, "stop_area:IDFM:70001")
	}
}

func TestPRIMClientDiscoversCombinationsHiddenByProviderOptimalityFiltering(t *testing.T) {
	const rerALineID = "line:IDFM:C01742"
	var mu sync.Mutex
	forbiddenPerRequest := make([][]string, 0)

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		forbidden := request.URL.Query()["forbidden_uris[]"]
		mu.Lock()
		forbiddenPerRequest = append(forbiddenPerRequest, forbidden)
		mu.Unlock()

		if slices.Contains(forbidden, rerALineID) {
			_, _ = writer.Write([]byte(`{"journeys":[{
				"departure_date_time":"20260803T181300","arrival_date_time":"20260803T190300",
				"sections":[
					{"type":"public_transport","departure_date_time":"20260803T181300","arrival_date_time":"20260803T183000","from":{"name":"Villiers"},"to":{"name":"Saint-Lazare"},"display_informations":{"physical_mode":"Métro","code":"3"},"links":[{"id":"line:IDFM:C01373","type":"line"}]},
					{"type":"public_transport","departure_date_time":"20260803T183500","arrival_date_time":"20260803T190300","from":{"name":"Saint-Lazare"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"Train Transilien","code":"L"},"links":[{"id":"line:IDFM:C01739","type":"line"}]}
				]}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"journeys":[{
			"departure_date_time":"20260803T180100","arrival_date_time":"20260803T184200",
			"sections":[
				{"type":"public_transport","departure_date_time":"20260803T180100","arrival_date_time":"20260803T181500","from":{"name":"Villiers"},"to":{"name":"Auber"},"display_informations":{"physical_mode":"Métro","code":"3"},"links":[{"id":"line:IDFM:C01373","type":"line"}]},
				{"type":"public_transport","departure_date_time":"20260803T182000","arrival_date_time":"20260803T184200","from":{"name":"Auber"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"RER","code":"A"},"links":[{"id":"` + rerALineID + `","type":"line"}]}
			]}]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}

	combinations := make([]string, 0, len(routes))
	for _, route := range routes {
		combinations = append(combinations, route.LineCombination())
	}
	if !slices.Contains(combinations, "Métro 3 + RER A") {
		t.Errorf("combinations = %v, want the provider's optimal journey", combinations)
	}
	if !slices.Contains(combinations, "Métro 3 + Train Transilien L") {
		t.Errorf("combinations = %v, want the combination hidden by optimality filtering", combinations)
	}

	mu.Lock()
	defer mu.Unlock()
	probedWithoutRERA := false
	for _, forbidden := range forbiddenPerRequest {
		if slices.Contains(forbidden, rerALineID) {
			probedWithoutRERA = true
		}
	}
	if !probedWithoutRERA {
		t.Error("no request forbade the RER A, so alternatives were never explored")
	}
	if got, want := len(forbiddenPerRequest), 1+len(departureSweepOffsets)+maxAlternativeLineExclusions; got > want {
		t.Errorf("provider calls = %d, want at most %d", got, want)
	}
}

func TestPRIMClientRetriesAFailedProbeSoItsCombinationDoesNotVanish(t *testing.T) {
	const rerA = "line:IDFM:C01742"
	var mu sync.Mutex
	probeAttempts := 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		if slices.Contains(request.URL.Query()["forbidden_uris[]"], rerA) {
			mu.Lock()
			probeAttempts++
			first := probeAttempts == 1
			mu.Unlock()
			if first {
				writer.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = writer.Write([]byte(`{"journeys":[{
				"departure_date_time":"20260803T181300","arrival_date_time":"20260803T190300",
				"sections":[
					{"type":"public_transport","departure_date_time":"20260803T181300","arrival_date_time":"20260803T190300","from":{"name":"Villiers"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"Train Transilien","code":"L"},"links":[{"id":"line:IDFM:C01740","type":"line"}]}
				]}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"journeys":[{
			"departure_date_time":"20260803T180100","arrival_date_time":"20260803T184200",
			"sections":[
				{"type":"public_transport","departure_date_time":"20260803T180100","arrival_date_time":"20260803T184200","from":{"name":"Villiers"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"RER","code":"A"},"links":[{"id":"` + rerA + `","type":"line"}]}
			]}]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}

	combinations := make([]string, 0, len(routes))
	for _, route := range routes {
		combinations = append(combinations, route.LineCombination())
	}
	if !slices.Contains(combinations, "Train Transilien L") {
		t.Errorf("combinations = %v; a probe that failed once erased the combination only it could reveal", combinations)
	}
}

func TestPRIMClientDoesNotRetryARateLimitedProbeAndWastesNoQuota(t *testing.T) {
	const rerA = "line:IDFM:C01742"
	var mu sync.Mutex
	probeAttempts := 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		if slices.Contains(request.URL.Query()["forbidden_uris[]"], rerA) {
			mu.Lock()
			probeAttempts++
			mu.Unlock()
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = writer.Write([]byte(`{"journeys":[{
			"departure_date_time":"20260803T180100","arrival_date_time":"20260803T184200",
			"sections":[
				{"type":"public_transport","departure_date_time":"20260803T180100","arrival_date_time":"20260803T184200","from":{"name":"Villiers"},"to":{"name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"RER","code":"A"},"links":[{"id":"` + rerA + `","type":"line"}]}
			]}]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	if _, err := client.Routes(context.Background(), routeRequest(t)); err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if probeAttempts != 1 {
		t.Errorf("rate limited probe attempted %d times, want 1; retrying a quota error burns calls that cannot succeed", probeAttempts)
	}
}

func TestPRIMClientCarriesProviderStationNamesSoAccentsSurvive(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"journeys":[{
			"departure_date_time":"20260803T180000","arrival_date_time":"20260803T182000",
			"sections":[
				{"type":"public_transport","departure_date_time":"20260803T180000","arrival_date_time":"20260803T182000","from":{"name":"Opéra (Paris)"},"to":{"name":"Bécon les Bruyères (Courbevoie)"},"display_informations":{"physical_mode":"Métro","code":"3"}}
			]}]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}
	if len(routes) == 0 {
		t.Fatal("Routes() returned no itinerary")
	}

	var checked bool
	for _, leg := range routes[0].Legs() {
		if leg.Mode() != "transit" {
			continue
		}
		checked = true
		if got, want := leg.FromName(), "Opéra (Paris)"; got != want {
			t.Errorf("leg.FromName() = %q, want %q; accents are lost when only the sanitised identifier is kept", got, want)
		}
		if got, want := leg.ToName(), "Bécon les Bruyères (Courbevoie)"; got != want {
			t.Errorf("leg.ToName() = %q, want %q", got, want)
		}
	}
	if !checked {
		t.Fatal("no transit leg was produced")
	}
}

func TestSimplifyPlaceQueryDropsTrailingQualifier(t *testing.T) {
	tests := []struct {
		name      string
		placeText string
		want      string
		wantOK    bool
	}{
		{name: "address with postcode", placeText: "10 Rue de Rivoli, 75004 Paris", want: "10 Rue de Rivoli", wantOK: true},
		{name: "no qualifier", placeText: "Cergy-le-Haut", wantOK: false},
		{name: "leading comma", placeText: ", 75004 Paris", wantOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := simplifyPlaceQuery(test.placeText)
			if ok != test.wantOK {
				t.Fatalf("simplifyPlaceQuery() ok = %v, want %v", ok, test.wantOK)
			}
			if ok && got != test.want {
				t.Errorf("simplifyPlaceQuery() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTransitLineIDsListsDistinctProviderLinesInOrder(t *testing.T) {
	journeys := []primJourney{{Sections: []primSection{
		{Type: sectionTypePublicTransport, Links: []primLink{{ID: "line:b", Type: linkTypeLine}}},
		{Type: sectionTypeTransfer, Links: []primLink{{ID: "line:ignored", Type: linkTypeLine}}},
		{Type: sectionTypePublicTransport, Links: []primLink{{ID: "line:a", Type: linkTypeLine}, {ID: "vj:x", Type: "vehicle_journey"}}},
		{Type: sectionTypePublicTransport, Links: []primLink{{ID: "line:b", Type: linkTypeLine}}},
	}}}

	got := transitLineIDs(journeys)
	want := []string{"line:b", "line:a"}
	if !slices.Equal(got, want) {
		t.Errorf("transitLineIDs() = %v, want %v", got, want)
	}
}

func TestTransitLabelTypePrefersPhysicalModeAndFallsBackToNetwork(t *testing.T) {
	tests := []struct {
		name    string
		display primDisplay
		want    string
	}{
		{
			name:    "physical mode",
			display: primDisplay{PhysicalMode: "Métro", Network: "Bus"},
			want:    "Métro",
		},
		{
			name:    "network fallback",
			display: primDisplay{PhysicalMode: " \t", Network: "RER"},
			want:    "RER",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := transitLabelType(test.display); got != test.want {
				t.Errorf("transitLabelType() = %q, want %q", got, test.want)
			}
		})
	}
}

func routeRequest(t *testing.T) app.RouteRequest {
	t.Helper()
	_, commute, err := app.NewCommute(app.CommuteInput{
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
	})
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	return app.RouteRequest{Commute: commute, Departure: time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)}
}

type countingTransport struct{ calls int }

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("network should not be called")
}

type responseTransport struct {
	body io.ReadCloser
}

func (t *responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: t.body}, nil
}

type oversizedBody struct {
	remaining int
	read      int
	closed    bool
}

func (b *oversizedBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		return 0, errors.New("response body exceeded discard limit")
	}
	buffer[0] = 0
	b.remaining--
	b.read++
	return 1, nil
}

func (b *oversizedBody) Close() error {
	b.closed = true
	return nil
}

func TestPRIMClientCarriesProviderIdentifiersSoAPatternCanBeDerived(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/places") {
			_, _ = writer.Write([]byte(`{"places":[{"id":"stop_area:IDFM:00001","embedded_type":"stop_area"}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"journeys":[{
			"departure_date_time":"20260803T180100","arrival_date_time":"20260803T190300",
			"sections":[
				{"type":"public_transport","departure_date_time":"20260803T180100","arrival_date_time":"20260803T183000","from":{"id":"stop_point:villiers","name":"Villiers"},"to":{"id":"stop_point:saint-lazare","name":"Saint-Lazare"},"display_informations":{"physical_mode":"Métro","code":"3"},"links":[{"id":"line:IDFM:C01373","type":"line"}]},
				{"type":"public_transport","departure_date_time":"20260803T183700","arrival_date_time":"20260803T190300","from":{"id":"stop_point:saint-lazare","name":"Gare Saint-Lazare"},"to":{"id":"stop_point:cergy","name":"Cergy-le-Haut"},"display_informations":{"physical_mode":"Train Transilien","code":"L"},"links":[{"id":"line:IDFM:C01740","type":"line"}]}
			]}]}`))
	}))
	defer server.Close()

	client := NewPRIMClient(server.URL+"/journeys", "api-key", server.Client())
	routes, err := client.Routes(context.Background(), routeRequest(t))
	if err != nil {
		t.Fatalf("Routes() error = %v, want nil", err)
	}
	if len(routes) == 0 {
		t.Fatal("Routes() returned no itinerary")
	}

	pattern, err := routes[0].RoutePattern()
	if err != nil {
		t.Fatalf("RoutePattern() error = %v; the provider identifiers never reached the itinerary", err)
	}
	legs := pattern.Legs()
	if got, want := len(legs), 2; got != want {
		t.Fatalf("pattern legs = %d, want %d", got, want)
	}
	if got, want := legs[0].Line(), domain.ProviderLineID("line:IDFM:C01373"); got != want {
		t.Errorf("legs[0].Line() = %q, want %q", got, want)
	}
	if got, want := legs[1].BoardAt(), domain.ProviderStopID("stop_point:saint-lazare"); got != want {
		t.Errorf("legs[1].BoardAt() = %q, want %q", got, want)
	}
	if got, want := legs[1].AlightAt(), domain.ProviderStopID("stop_point:cergy"); got != want {
		t.Errorf("legs[1].AlightAt() = %q, want %q", got, want)
	}
}
