package domain

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidRoutePattern = errors.New("invalid route pattern")

type ProviderLineID string
type ProviderStopID string

// PatternLeg names one ride by provider identifiers. Display labels never take
// part in matching: a line code is ambiguous across branches, an ID is not.
type PatternLeg struct {
	line       ProviderLineID
	boardAt    ProviderStopID
	alightAt   ProviderStopID
	boardName  string
	alightName string
	label      TransitLabel
}

func NewPatternLeg(line ProviderLineID, boardAt, alightAt ProviderStopID, label TransitLabel) (PatternLeg, error) {
	return NewPatternLegWithNames(line, boardAt, alightAt, "", "", label)
}

func NewPatternLegWithNames(line ProviderLineID, boardAt, alightAt ProviderStopID, boardName, alightName string, label TransitLabel) (PatternLeg, error) {
	if strings.TrimSpace(string(line)) == "" ||
		strings.TrimSpace(string(boardAt)) == "" ||
		strings.TrimSpace(string(alightAt)) == "" ||
		boardAt == alightAt {
		return PatternLeg{}, ErrInvalidRoutePattern
	}
	return PatternLeg{
		line:       line,
		boardAt:    boardAt,
		alightAt:   alightAt,
		boardName:  strings.TrimSpace(boardName),
		alightName: strings.TrimSpace(alightName),
		label:      label,
	}, nil
}

func (l PatternLeg) Line() ProviderLineID     { return l.line }
func (l PatternLeg) BoardAt() ProviderStopID  { return l.boardAt }
func (l PatternLeg) AlightAt() ProviderStopID { return l.alightAt }
func (l PatternLeg) BoardName() string        { return l.boardName }
func (l PatternLeg) AlightName() string       { return l.alightName }
func (l PatternLeg) Label() TransitLabel      { return l.label }

func (l PatternLeg) valid() bool {
	return strings.TrimSpace(string(l.line)) != "" &&
		strings.TrimSpace(string(l.boardAt)) != "" &&
		strings.TrimSpace(string(l.alightAt)) != "" &&
		l.boardAt != l.alightAt
}

type RoutePattern struct {
	legs []PatternLeg
}

func NewRoutePattern(legs []PatternLeg) (RoutePattern, error) {
	if len(legs) == 0 {
		return RoutePattern{}, ErrInvalidRoutePattern
	}
	for _, leg := range legs {
		if !leg.valid() {
			return RoutePattern{}, ErrInvalidRoutePattern
		}
	}
	return RoutePattern{legs: append([]PatternLeg(nil), legs...)}, nil
}

func (p RoutePattern) Legs() []PatternLeg {
	return append([]PatternLeg(nil), p.legs...)
}

func (p RoutePattern) Clone() RoutePattern {
	return RoutePattern{legs: p.Legs()}
}

// Fingerprint is the canonical provider identity for a route. It deliberately
// excludes display labels: labels may change and are ambiguous across branches.
// Length-prefixed fields avoid collisions when provider IDs contain separators.
func (p RoutePattern) Fingerprint() string {
	if len(p.legs) == 0 {
		return ""
	}
	var fingerprint strings.Builder
	for _, leg := range p.legs {
		if !leg.valid() {
			return ""
		}
		appendFingerprintField(&fingerprint, string(leg.line))
		appendFingerprintField(&fingerprint, string(leg.boardAt))
		appendFingerprintField(&fingerprint, string(leg.alightAt))
	}
	return fingerprint.String()
}

func (p RoutePattern) Equal(other RoutePattern) bool {
	fingerprint := p.Fingerprint()
	return fingerprint != "" && fingerprint == other.Fingerprint()
}

func appendFingerprintField(fingerprint *strings.Builder, value string) {
	fingerprint.WriteString(strconv.Itoa(len(value)))
	fingerprint.WriteByte(':')
	fingerprint.WriteString(value)
}

func (p RoutePattern) Combination() string {
	labels := make([]string, 0, len(p.legs))
	for _, leg := range p.legs {
		parts := make([]string, 0, 2)
		if network := strings.TrimSpace(leg.label.Network()); network != "" {
			parts = append(parts, network)
		}
		if code := strings.TrimSpace(leg.label.Code()); code != "" {
			parts = append(parts, code)
		}
		if joined := strings.Join(parts, " "); joined != "" {
			labels = append(labels, joined)
		}
	}
	return strings.Join(labels, " + ")
}

// RoutePattern derives the exact pattern of an itinerary. It fails when any
// transit leg lacks provider identifiers, because planning on display labels
// alone cannot tell two branches of the same line apart.
func (i Itinerary) RoutePattern() (RoutePattern, error) {
	legs := make([]PatternLeg, 0, len(i.legs))
	for _, leg := range i.legs {
		if !leg.isTransit() {
			continue
		}
		patternLeg, err := NewPatternLegWithNames(
			leg.providerLine,
			leg.providerBoardAt,
			leg.providerAlightAt,
			leg.FromName(),
			leg.ToName(),
			leg.transitLabel,
		)
		if err != nil {
			return RoutePattern{}, err
		}
		legs = append(legs, patternLeg)
	}
	return NewRoutePattern(legs)
}
