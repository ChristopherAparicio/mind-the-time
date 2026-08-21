package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const linkTypeVehicleJourney = "vehicle_journey"

// One hour of Métro 3 is already 1.2 MB: the journey-sized cap truncated it into invalid JSON.
const maxTimetableBodyBytes = 16 << 20

type primRouteSchedules struct {
	RouteSchedules []primRouteSchedule `json:"route_schedules"`
}

type primRouteSchedule struct {
	Table primScheduleTable `json:"table"`
}

type primScheduleTable struct {
	Rows []primScheduleRow `json:"rows"`
}

type primScheduleRow struct {
	StopPoint primPlace          `json:"stop_point"`
	DateTimes []primScheduleCell `json:"date_times"`
}

type primScheduleCell struct {
	DateTime string     `json:"date_time"`
	Links    []primLink `json:"links"`
}

func (c primScheduleCell) vehicleJourney() string {
	for _, link := range c.Links {
		if link.Type == linkTypeVehicleJourney {
			return strings.TrimSpace(link.ID)
		}
	}
	return ""
}

// TimetableSnapshot reads one timetable grid per pattern leg. A grid lists every
// train against every stop, so the whole plan needs one call per line instead of
// one per candidate train.
func (c *PRIMClient) TimetableSnapshot(
	ctx context.Context,
	pattern domain.RoutePattern,
	from time.Time,
	window time.Duration,
) (domain.TimetableSnapshot, error) {
	legs := make([][]domain.ObservedConnection, 0, len(pattern.Legs()))
	for _, leg := range pattern.Legs() {
		schedules, err := c.fetchRouteSchedules(ctx, leg.Line(), from, window)
		if err != nil {
			return domain.TimetableSnapshot{}, err
		}
		connections, err := connectionsForLeg(schedules, leg)
		if err != nil {
			return domain.TimetableSnapshot{}, err
		}
		legs = append(legs, connections)
	}
	return domain.NewTimetableSnapshot(legs)
}

func (c *PRIMClient) fetchRouteSchedules(
	ctx context.Context,
	line domain.ProviderLineID,
	from time.Time,
	window time.Duration,
) (primRouteSchedules, error) {
	endpoint, err := url.ParseRequestURI(c.endpoint)
	if err != nil || !strings.Contains(endpoint.Path, "/journeys") {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}
	base := strings.TrimSuffix(endpoint.String(), "/journeys")
	target, err := url.Parse(fmt.Sprintf("%s/lines/%s/route_schedules", base, url.PathEscape(string(line))))
	if err != nil {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}
	query := target.Query()
	query.Set("from_datetime", from.Format(primTimeLayout))
	query.Set("duration", fmt.Sprintf("%d", int(window.Seconds())))
	target.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}
	request.Header.Set("apiKey", c.apiKey)

	response, err := c.client.Do(request)
	if err != nil {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusTooManyRequests {
		return primRouteSchedules{}, app.ErrRateLimited
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTimetableBodyBytes))
	if err != nil {
		return primRouteSchedules{}, app.ErrProviderUnavailable
	}
	var decoded primRouteSchedules
	if err := json.Unmarshal(body, &decoded); err != nil {
		return primRouteSchedules{}, app.ErrProviderUnsupportedContract
	}
	return decoded, nil
}

// A line is published as parallel routes each carrying its own trains: Auber to
// Cergy-le-Haut spans two RER A routes of 7 and 8 trains, so keeping only the
// first match would drop half the departures.
//
// No connection is not a broken contract: the provider answers 200 with the full
// stop list and no departure at all when the window falls outside the service.
func connectionsForLeg(schedules primRouteSchedules, leg domain.PatternLeg) ([]domain.ObservedConnection, error) {
	connections := make([]domain.ObservedConnection, 0)
	seen := make(map[domain.ObservationID]struct{})
	for _, schedule := range schedules.RouteSchedules {
		boardRow, alightRow, ok := orderedRowsFor(schedule.Table.Rows, leg)
		if !ok {
			continue
		}
		for _, connection := range pairSameVehicleJourney(boardRow, alightRow, leg) {
			if _, duplicate := seen[connection.Observation()]; duplicate {
				continue
			}
			seen[connection.Observation()] = struct{}{}
			connections = append(connections, connection)
		}
	}
	if len(connections) == 0 {
		return nil, app.ErrNoTimetableCoverage
	}
	return connections, nil
}

// orderedRowsFor requires the boarding stop to appear before the alighting stop.
// Direction labels are unreliable: the branch serving Cergy-le-Haut announces
// itself as "Nanterre Université".
func orderedRowsFor(rows []primScheduleRow, leg domain.PatternLeg) (primScheduleRow, primScheduleRow, bool) {
	boardIndex, alightIndex := -1, -1
	for index, row := range rows {
		switch domain.ProviderStopID(strings.TrimSpace(row.StopPoint.ID)) {
		case leg.BoardAt():
			if boardIndex < 0 {
				boardIndex = index
			}
		case leg.AlightAt():
			if alightIndex < 0 {
				alightIndex = index
			}
		}
	}
	if boardIndex < 0 || alightIndex < 0 || boardIndex >= alightIndex {
		return primScheduleRow{}, primScheduleRow{}, false
	}
	return rows[boardIndex], rows[alightIndex], true
}

// pairSameVehicleJourney only pairs a departure with an arrival when both cells
// belong to the same run, so no connection is ever invented from two trains.
func pairSameVehicleJourney(boardRow, alightRow primScheduleRow, leg domain.PatternLeg) []domain.ObservedConnection {
	alightByJourney := make(map[string]primScheduleCell, len(alightRow.DateTimes))
	for _, cell := range alightRow.DateTimes {
		if journey := cell.vehicleJourney(); journey != "" {
			alightByJourney[journey] = cell
		}
	}

	connections := make([]domain.ObservedConnection, 0, len(boardRow.DateTimes))
	for _, boardCell := range boardRow.DateTimes {
		journey := boardCell.vehicleJourney()
		alightCell, paired := alightByJourney[journey]
		if journey == "" || !paired {
			continue
		}
		departure, departureErr := parsePRIMTime(boardCell.DateTime)
		arrival, arrivalErr := parsePRIMTime(alightCell.DateTime)
		if departureErr != nil || arrivalErr != nil || !arrival.After(departure) {
			continue
		}
		boardID, boardErr := domain.NewStationID(cleanStationID(boardRow.StopPoint.Name, boardRow.StopPoint.ID))
		alightID, alightErr := domain.NewStationID(cleanStationID(alightRow.StopPoint.Name, alightRow.StopPoint.ID))
		if boardErr != nil || alightErr != nil {
			continue
		}
		connection, err := domain.NewObservedConnection(
			domain.ObservationID(journey), leg.Label(),
			boardID, alightID,
			boardRow.StopPoint.Name, alightRow.StopPoint.Name,
			departure, arrival,
		)
		if err != nil {
			continue
		}
		connections = append(connections, connection)
	}
	return connections
}
