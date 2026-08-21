package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidStationID = errors.New("invalid station ID")
	ErrInvalidLeg       = errors.New("invalid itinerary leg")
	ErrInvalidItinerary = errors.New("invalid itinerary")
)

const SaintLazareStationID = "saint-lazare"

type StationID string

func NewStationID(id string) (StationID, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return "", ErrInvalidStationID
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return "", ErrInvalidStationID
		}
	}
	return StationID(id), nil
}

func (id StationID) String() string {
	return string(id)
}

func (id StationID) valid() bool {
	normalized, err := NewStationID(string(id))
	return err == nil && normalized == id
}

type legMode uint8

const (
	walkingLeg legMode = iota + 1
	transitLeg
)

type ItineraryLeg struct {
	mode             legMode
	from             StationID
	to               StationID
	fromName         string
	toName           string
	departure        time.Time
	arrival          time.Time
	transitLabel     TransitLabel
	providerLine     ProviderLineID
	providerBoardAt  ProviderStopID
	providerAlightAt ProviderStopID
}

type TransitLabel struct {
	network string
	code    string
}

func NewTransitLabel(network, code string) TransitLabel {
	return TransitLabel{network: strings.TrimSpace(network), code: strings.TrimSpace(code)}
}

func (l TransitLabel) Network() string {
	return l.network
}

func (l TransitLabel) Code() string {
	return l.code
}

func (l TransitLabel) empty() bool {
	return l.network == "" && l.code == ""
}

func NewWalkingLeg(departure, arrival time.Time) (ItineraryLeg, error) {
	if !validLegTimes(departure, arrival) {
		return ItineraryLeg{}, ErrInvalidLeg
	}
	return ItineraryLeg{mode: walkingLeg, departure: departure, arrival: arrival}, nil
}

func NewTransitLeg(from, to StationID, departure, arrival time.Time) (ItineraryLeg, error) {
	if !from.valid() || !to.valid() || !validLegTimes(departure, arrival) {
		return ItineraryLeg{}, ErrInvalidLeg
	}
	return ItineraryLeg{mode: transitLeg, from: from, to: to, departure: departure, arrival: arrival}, nil
}

func NewTransitLegWithLabel(from, to StationID, departure, arrival time.Time, label TransitLabel) (ItineraryLeg, error) {
	leg, err := NewTransitLeg(from, to, departure, arrival)
	if err != nil {
		return ItineraryLeg{}, err
	}
	leg.transitLabel = label
	return leg, nil
}

// Station identifiers are sanitised to a-z0-9, so accents only survive in these names.
func NewTransitLegWithStations(from, to StationID, fromName, toName string, departure, arrival time.Time, label TransitLabel) (ItineraryLeg, error) {
	leg, err := NewTransitLegWithLabel(from, to, departure, arrival, label)
	if err != nil {
		return ItineraryLeg{}, err
	}
	leg.fromName = strings.TrimSpace(fromName)
	leg.toName = strings.TrimSpace(toName)
	return leg, nil
}

// A line code cannot tell the Cergy branch of line L from the Versailles one; an ID can.
func (l ItineraryLeg) withProviderIdentity(line ProviderLineID, boardAt, alightAt ProviderStopID) ItineraryLeg {
	l.providerLine = line
	l.providerBoardAt = boardAt
	l.providerAlightAt = alightAt
	return l
}

func NewTransitLegWithProviderIdentity(
	from, to StationID,
	fromName, toName string,
	departure, arrival time.Time,
	label TransitLabel,
	line ProviderLineID,
	boardAt, alightAt ProviderStopID,
) (ItineraryLeg, error) {
	leg, err := NewTransitLegWithStations(from, to, fromName, toName, departure, arrival, label)
	if err != nil {
		return ItineraryLeg{}, err
	}
	return leg.withProviderIdentity(line, boardAt, alightAt), nil
}

func (l ItineraryLeg) FromName() string {
	return l.fromName
}

func (l ItineraryLeg) ToName() string {
	return l.toName
}

func validLegTimes(departure, arrival time.Time) bool {
	return !departure.IsZero() && !arrival.IsZero() && !arrival.Before(departure)
}

func (l ItineraryLeg) isTransit() bool {
	return l.mode == transitLeg
}

func (l ItineraryLeg) isWalking() bool {
	return l.mode == walkingLeg
}

type Itinerary struct {
	legs []ItineraryLeg
}

func NewItinerary(legs []ItineraryLeg) (Itinerary, error) {
	if len(legs) == 0 {
		return Itinerary{}, ErrInvalidItinerary
	}
	for index, leg := range legs {
		if !leg.valid() || (index > 0 && leg.departure.Before(legs[index-1].arrival)) {
			return Itinerary{}, ErrInvalidItinerary
		}
	}
	return Itinerary{legs: append([]ItineraryLeg(nil), legs...)}, nil
}

func (l ItineraryLeg) Mode() string {
	if l.isWalking() {
		return "walking"
	}
	return "transit"
}

func (l ItineraryLeg) From() StationID {
	return l.from
}

func (l ItineraryLeg) To() StationID {
	return l.to
}

func (l ItineraryLeg) Departure() time.Time {
	return l.departure
}

func (l ItineraryLeg) Arrival() time.Time {
	return l.arrival
}

func (l ItineraryLeg) Duration() time.Duration {
	return l.arrival.Sub(l.departure)
}

func (l ItineraryLeg) TransitLabel() (TransitLabel, bool) {
	if !l.isTransit() || l.transitLabel.empty() {
		return TransitLabel{}, false
	}
	return l.transitLabel, true
}

func (i Itinerary) Legs() []ItineraryLeg {
	return append([]ItineraryLeg(nil), i.legs...)
}

func (l ItineraryLeg) valid() bool {
	if !validLegTimes(l.departure, l.arrival) {
		return false
	}
	if l.isWalking() {
		return l.from == "" && l.to == ""
	}
	return l.isTransit() && l.from.valid() && l.to.valid()
}

func (i Itinerary) Departure() time.Time {
	if len(i.legs) == 0 {
		return time.Time{}
	}
	return i.legs[0].departure
}

func (i Itinerary) Arrival() time.Time {
	if len(i.legs) == 0 {
		return time.Time{}
	}
	return i.legs[len(i.legs)-1].arrival
}

func (i Itinerary) Duration() time.Duration {
	if len(i.legs) == 0 {
		return 0
	}
	return i.Arrival().Sub(i.Departure())
}

func (i Itinerary) Transfers() int {
	transitLegs := 0
	for _, leg := range i.legs {
		if leg.isTransit() {
			transitLegs++
		}
	}
	if transitLegs == 0 {
		return 0
	}
	return transitLegs - 1
}

// Personal walking and interchange durations shift the departure time; they never discard provider results.
func (i Itinerary) IsEligibleFor(commute Commute) bool {
	if !commute.valid() || len(i.legs) == 0 || !i.hasTransitLeg() {
		return false
	}
	return commute.preferredLines.MatchesItinerary(i)
}

func (i Itinerary) LeaveAt(accessToFirstBoard TransferBuffer) time.Time {
	for _, leg := range i.legs {
		if leg.isTransit() {
			return leg.departure.Add(-accessToFirstBoard.duration)
		}
	}
	return i.Departure()
}

func (i Itinerary) TightestInterchange() (time.Duration, bool) {
	transfers := i.transitTransfers()
	if len(transfers) == 0 {
		return 0, false
	}
	tightest := transfers[0].gap
	for _, transfer := range transfers[1:] {
		if transfer.gap < tightest {
			tightest = transfer.gap
		}
	}
	return tightest, true
}

func (i Itinerary) InterchangeFits(interchange TransferBuffer) bool {
	tightest, hasTransfer := i.TightestInterchange()
	return !hasTransfer || tightest >= interchange.duration
}

type transitTransfer struct {
	arriving  ItineraryLeg
	departing ItineraryLeg
	gap       time.Duration
}

func (i Itinerary) transitTransfers() []transitTransfer {
	transfers := make([]transitTransfer, 0, i.Transfers())
	var arriving ItineraryLeg
	hasArriving := false
	for _, leg := range i.legs {
		if !leg.isTransit() {
			continue
		}
		if hasArriving {
			transfers = append(transfers, transitTransfer{
				arriving:  arriving,
				departing: leg,
				gap:       leg.departure.Sub(arriving.arrival),
			})
		}
		arriving = leg
		hasArriving = true
	}
	return transfers
}

func (i Itinerary) hasTransitLeg() bool {
	for _, leg := range i.legs {
		if leg.isTransit() {
			return true
		}
	}
	return false
}

func (i Itinerary) LineCombination() string {
	var lines []string
	for _, leg := range i.legs {
		if !leg.isTransit() {
			continue
		}
		if label, ok := leg.TransitLabel(); ok {
			var parts []string
			if net := strings.TrimSpace(label.Network()); net != "" {
				parts = append(parts, net)
			}
			if code := strings.TrimSpace(label.Code()); code != "" {
				parts = append(parts, code)
			}
			lineName := strings.Join(parts, " ")
			if lineName != "" {
				lines = append(lines, lineName)
			}
		}
	}
	if len(lines) == 0 {
		return "Transports en commun"
	}
	return strings.Join(lines, " + ")
}
