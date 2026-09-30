package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const (
	maxResponseBodyBytes = 1 << 20
	primTimeLayout       = "20060102T150405"

	sectionTypePublicTransport = "public_transport"
	sectionTypeStreetNetwork   = "street_network"
	sectionTypeWalking         = "walking"
	sectionTypeTransfer        = "transfer"
	linkTypeLine               = "line"

	// maxAlternativeLineExclusions bounds how many "same trip without this line"
	// probes are sent per evaluation, keeping the provider call budget small and
	// predictable.
	maxAlternativeLineExclusions = 3
	discoveryProbeAttempts       = 2
)

// departureSweepOffsets shifts the requested departure time. The provider's
// optimal set depends on the real timetable, so a later departure sometimes
// exposes combinations that were dominated a few minutes earlier.
var departureSweepOffsets = []time.Duration{10 * time.Minute, 20 * time.Minute}

type PRIMClient struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewPRIMClient(endpoint, apiKey string, client *http.Client) *PRIMClient {
	return &PRIMClient{
		endpoint: strings.TrimSpace(endpoint),
		apiKey:   strings.TrimSpace(apiKey),
		client:   client,
	}
}

type primResponse struct {
	Journeys []primJourney `json:"journeys"`
}

type primJourney struct {
	DepartureDateTime string        `json:"departure_date_time"`
	ArrivalDateTime   string        `json:"arrival_date_time"`
	Duration          int64         `json:"duration"`
	Sections          []primSection `json:"sections"`
}

type primSection struct {
	Type              string      `json:"type"`
	Mode              string      `json:"mode"`
	TransferType      string      `json:"transfer_type"`
	DepartureDateTime string      `json:"departure_date_time"`
	ArrivalDateTime   string      `json:"arrival_date_time"`
	From              primPlace   `json:"from"`
	To                primPlace   `json:"to"`
	DisplayDef        primDisplay `json:"display_informations"`
	Links             []primLink  `json:"links"`
}

type primPlace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type primDisplay struct {
	PhysicalMode string `json:"physical_mode"`
	Network      string `json:"network"`
	Code         string `json:"code"`
}

type primLink struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type journeyQuery struct {
	originID         string
	destinationID    string
	departure        time.Time
	forbiddenLineIDs []string
}

func (q journeyQuery) withDepartureOffset(offset time.Duration) journeyQuery {
	q.departure = q.departure.Add(offset)
	return q
}

func (q journeyQuery) withoutLine(lineID string) journeyQuery {
	q.forbiddenLineIDs = []string{lineID}
	return q
}

func (c *PRIMClient) Routes(ctx context.Context, routeRequest app.RouteRequest) ([]domain.Itinerary, error) {
	if routeRequest.Departure.IsZero() {
		return nil, app.ErrProviderUnavailable
	}
	if c.endpoint == "" || c.apiKey == "" || c.client == nil {
		return nil, app.ErrProviderUnavailable
	}
	endpoint, err := url.ParseRequestURI(c.endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, app.ErrProviderUnavailable
	}

	baseQuery := journeyQuery{
		originID:      c.resolvePlaceID(ctx, routeRequest.Commute.Origin().Text()),
		destinationID: c.resolvePlaceID(ctx, routeRequest.Commute.Destination().Text()),
		departure:     routeRequest.Departure,
	}

	journeys, err := c.discoverJourneys(ctx, endpoint, baseQuery)
	if err != nil {
		return nil, err
	}
	return distinctItineraries(journeys), nil
}

// discoverJourneys works around a structural behaviour of the provider: it only
// returns Pareto-optimal journeys, so a perfectly valid combination that is a
// few minutes slower is never proposed. Two complementary strategies widen the
// result set, and both rely exclusively on data the provider actually returns:
//   - sweeping the departure time, because the optimal set follows the real
//     timetable and widens at some minutes;
//   - forbidding, one line at a time, the lines already proposed, which makes
//     the provider compute the best journey that avoids them.
func (c *PRIMClient) discoverJourneys(ctx context.Context, endpoint *url.URL, base journeyQuery) ([]primJourney, error) {
	discovered, err := c.fetchJourneys(ctx, endpoint, base)
	if err != nil {
		return nil, err
	}

	for _, offset := range departureSweepOffsets {
		discovered = append(discovered, c.probeJourneys(ctx, endpoint, base.withDepartureOffset(offset))...)
	}

	for _, lineID := range firstStrings(transitLineIDs(discovered), maxAlternativeLineExclusions) {
		discovered = append(discovered, c.probeJourneys(ctx, endpoint, base.withoutLine(lineID))...)
	}

	return discovered, nil
}

func sectionLineID(section primSection) string {
	for _, link := range section.Links {
		if link.Type == linkTypeLine {
			return strings.TrimSpace(link.ID)
		}
	}
	return ""
}

func transitLineIDs(journeys []primJourney) []string {
	seen := make(map[string]struct{})
	lineIDs := make([]string, 0, len(journeys))
	for _, journey := range journeys {
		for _, section := range journey.Sections {
			if section.Type != sectionTypePublicTransport {
				continue
			}
			for _, link := range section.Links {
				lineID := strings.TrimSpace(link.ID)
				if link.Type != linkTypeLine || lineID == "" {
					continue
				}
				if _, exists := seen[lineID]; exists {
					continue
				}
				seen[lineID] = struct{}{}
				lineIDs = append(lineIDs, lineID)
			}
		}
	}
	return lineIDs
}

func firstStrings(values []string, limit int) []string {
	if limit < 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func distinctItineraries(journeys []primJourney) []domain.Itinerary {
	var itineraries []domain.Itinerary
	seenKey := make(map[string]struct{}, len(journeys))
	for _, journey := range journeys {
		itinerary, ok := itineraryFromJourney(journey)
		if !ok {
			continue
		}
		key := strings.Join([]string{
			itinerary.Departure().Format(time.RFC3339),
			itinerary.Arrival().Format(time.RFC3339),
			itinerary.LineCombination(),
		}, "_")
		if _, exists := seenKey[key]; exists {
			continue
		}
		seenKey[key] = struct{}{}
		itineraries = append(itineraries, itinerary)
	}
	return itineraries
}

func itineraryFromJourney(journey primJourney) (domain.Itinerary, bool) {
	legs := legsFromSections(journey.Sections)
	if len(legs) == 0 {
		legs = directPathLegs(journey)
	}
	if len(legs) == 0 {
		return domain.Itinerary{}, false
	}
	itinerary, err := domain.NewItinerary(legs)
	if err != nil {
		return domain.Itinerary{}, false
	}
	return itinerary, true
}

func legsFromSections(sections []primSection) []domain.ItineraryLeg {
	legs := make([]domain.ItineraryLeg, 0, len(sections))
	for _, section := range sections {
		departure, departureErr := parsePRIMTime(section.DepartureDateTime)
		arrival, arrivalErr := parsePRIMTime(section.ArrivalDateTime)
		if departureErr != nil || arrivalErr != nil {
			continue
		}
		leg, ok := legFromSection(section, departure, arrival)
		if ok {
			legs = append(legs, leg)
		}
	}
	return legs
}

func legFromSection(section primSection, departure, arrival time.Time) (domain.ItineraryLeg, bool) {
	switch section.Type {
	case sectionTypeStreetNetwork, sectionTypeWalking, sectionTypeTransfer:
		if !isWalkingSection(section) {
			return domain.ItineraryLeg{}, false
		}
		leg, err := domain.NewWalkingLeg(departure, arrival)
		return leg, err == nil
	case sectionTypePublicTransport:
		from, fromErr := domain.NewStationID(cleanStationID(section.From.Name, section.From.ID))
		to, toErr := domain.NewStationID(cleanStationID(section.To.Name, section.To.ID))
		if fromErr != nil || toErr != nil {
			return domain.ItineraryLeg{}, false
		}
		label := domain.NewTransitLabel(transitLabelType(section.DisplayDef), section.DisplayDef.Code)
		leg, err := domain.NewTransitLegWithProviderIdentity(
			from, to, section.From.Name, section.To.Name, departure, arrival, label,
			domain.ProviderLineID(sectionLineID(section)),
			domain.ProviderStopID(strings.TrimSpace(section.From.ID)),
			domain.ProviderStopID(strings.TrimSpace(section.To.ID)),
		)
		return leg, err == nil
	default:
		return domain.ItineraryLeg{}, false
	}
}

func directPathLegs(journey primJourney) []domain.ItineraryLeg {
	departure, departureErr := parsePRIMTime(journey.DepartureDateTime)
	arrival, arrivalErr := parsePRIMTime(journey.ArrivalDateTime)
	if departureErr != nil || arrivalErr != nil {
		return nil
	}
	leg, err := domain.NewWalkingLeg(departure, arrival)
	if err != nil {
		return nil
	}
	return []domain.ItineraryLeg{leg}
}

func (c *PRIMClient) fetchJourneys(ctx context.Context, endpoint *url.URL, query journeyQuery) ([]primJourney, error) {
	request, err := c.newJourneysRequest(ctx, endpoint, query)
	if err != nil {
		return nil, err
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("provider request: %w", app.ErrProviderUnavailable)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusTooManyRequests {
		return nil, app.ErrRateLimited
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, app.ErrProviderUnavailable
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read provider response: %w", app.ErrProviderUnavailable)
	}

	var primData primResponse
	if err := json.Unmarshal(bodyBytes, &primData); err != nil {
		return nil, app.ErrProviderUnsupportedContract
	}
	return primData.Journeys, nil
}

// A silently failing probe erases the combinations only it could reveal, which the caller then reports as non-existent.
func (c *PRIMClient) probeJourneys(ctx context.Context, endpoint *url.URL, query journeyQuery) []primJourney {
	for attempt := 0; attempt < discoveryProbeAttempts; attempt++ {
		journeys, err := c.fetchJourneys(ctx, endpoint, query)
		if err == nil {
			return journeys
		}
		// Retrying a rate limit burns a second call for a request that cannot succeed.
		if ctx.Err() != nil || errors.Is(err, app.ErrRateLimited) {
			return nil
		}
	}
	return nil
}

func (c *PRIMClient) newJourneysRequest(ctx context.Context, endpoint *url.URL, journey journeyQuery) (*http.Request, error) {
	requestURL := *endpoint
	query := requestURL.Query()
	query.Set("from", journey.originID)
	query.Set("to", journey.destinationID)
	query.Set("datetime", journey.departure.Format(primTimeLayout))
	query.Set("datetime_represents", "departure")
	for _, lineID := range journey.forbiddenLineIDs {
		query.Add("forbidden_uris[]", lineID)
	}
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create provider request: %w", app.ErrProviderUnavailable)
	}
	request.Header.Set("apiKey", c.apiKey)
	return request, nil
}

// resolvePlaceID turns free text into a provider place identifier. The provider
// ranks /places results by relevance, so the first result is the right answer:
// a street address resolves to its own coordinates and a station name resolves
// to its stop area. Preferring a stop area unconditionally would silently move
// the user's doorstep to an unrelated nearby station.
func (c *PRIMClient) resolvePlaceID(ctx context.Context, placeText string) string {
	placeText = strings.TrimSpace(placeText)
	if placeText == "" {
		return ""
	}
	if isProviderPlaceID(placeText) {
		return placeText
	}
	if placeID, err := c.queryPlaceID(ctx, placeText); err == nil {
		return placeID
	}
	if simplified, ok := simplifyPlaceQuery(placeText); ok {
		if placeID, err := c.queryPlaceID(ctx, simplified); err == nil {
			return placeID
		}
	}
	return placeText
}

func isProviderPlaceID(placeText string) bool {
	if strings.Contains(placeText, ";") {
		return true
	}
	for _, prefix := range []string{"stop_area:", "stop_point:", "admin:", "poi:", "address:"} {
		if strings.HasPrefix(placeText, prefix) {
			return true
		}
	}
	return false
}

func simplifyPlaceQuery(placeText string) (string, bool) {
	head, _, found := strings.Cut(placeText, ",")
	head = strings.TrimSpace(head)
	if !found || head == "" {
		return "", false
	}
	return head, true
}

func (c *PRIMClient) queryPlaceID(ctx context.Context, queryText string) (string, error) {
	endpoint, err := url.ParseRequestURI(c.endpoint)
	if err != nil || !strings.Contains(endpoint.Path, "/journeys") {
		return "", fmt.Errorf("invalid endpoint")
	}
	placesEndpoint, err := url.Parse(strings.Replace(endpoint.String(), "/journeys", "/places", 1))
	if err != nil {
		return "", err
	}
	query := placesEndpoint.Query()
	query.Set("q", queryText)
	placesEndpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, placesEndpoint.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("apiKey", c.apiKey)

	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("places status: %d", response.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return "", err
	}
	var decoded struct {
		Places []struct {
			ID string `json:"id"`
		} `json:"places"`
	}
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		return "", fmt.Errorf("decode places response: %w", err)
	}
	for _, place := range decoded.Places {
		if placeID := strings.TrimSpace(place.ID); placeID != "" {
			return placeID, nil
		}
	}
	return "", fmt.Errorf("no place found for %q", queryText)
}

func isWalkingSection(section primSection) bool {
	return section.Mode == sectionTypeWalking || section.TransferType == sectionTypeWalking
}

func transitLabelType(display primDisplay) string {
	if strings.TrimSpace(display.PhysicalMode) != "" {
		return display.PhysicalMode
	}
	return display.Network
}

func parsePRIMTime(val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if len(val) < 15 {
		return time.Time{}, fmt.Errorf("invalid time format")
	}
	paris, err := domain.NetworkLocation()
	if err != nil {
		paris = time.UTC
	}
	return time.ParseInLocation(primTimeLayout, val[:15], paris)
}

func cleanStationID(name, fallbackID string) string {
	cleaned := strings.ToLower(strings.TrimSpace(name))
	if cleaned == "" {
		cleaned = strings.ToLower(strings.TrimSpace(fallbackID))
	}
	var builder strings.Builder
	for _, r := range cleaned {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else if r == '-' || r == ' ' || r == '_' || r == '\'' {
			builder.WriteRune('-')
		}
	}
	res := strings.Trim(builder.String(), "-")
	if res == "" {
		return "station"
	}
	return res
}
