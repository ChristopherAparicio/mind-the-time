package domain

import (
	"strings"
)

type PreferredLines struct {
	raw   string
	codes []string
}

func NewPreferredLines(input string) PreferredLines {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return PreferredLines{}
	}
	normalizedInput := strings.ReplaceAll(trimmed, "+", ",")
	parts := strings.Split(normalizedInput, ",")
	codes := make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned := strings.ToLower(strings.TrimSpace(part))
		if cleaned != "" {
			codes = append(codes, cleaned)
		}
	}
	return PreferredLines{raw: trimmed, codes: codes}
}

func (p PreferredLines) Raw() string {
	return p.raw
}

func (p PreferredLines) Empty() bool {
	return len(p.codes) == 0
}

func (p PreferredLines) MatchesLeg(leg ItineraryLeg) bool {
	if p.Empty() {
		return true
	}
	if !leg.isTransit() {
		return true
	}
	// An unlabelled leg carries no line to compare, so it cannot veto a selection.
	if _, labelled := leg.TransitLabel(); !labelled {
		return true
	}
	for _, code := range p.codes {
		if legMatchesCode(leg, code) {
			return true
		}
	}
	return false
}

func (p PreferredLines) MatchesItinerary(itinerary Itinerary) bool {
	if p.Empty() {
		return true
	}
	for _, leg := range itinerary.legs {
		if leg.isTransit() && !p.MatchesLeg(leg) {
			return false
		}
	}
	return true
}

func legMatchesCode(leg ItineraryLeg, code string) bool {
	label, ok := leg.TransitLabel()
	if !ok {
		return false
	}
	network := strings.ToLower(strings.TrimSpace(label.Network()))
	codeVal := strings.ToLower(strings.TrimSpace(label.Code()))
	fullLabel := strings.TrimSpace(network + " " + codeVal)

	// Substring matching made "Métro 1" accept a Métro 13 leg, and "3" accept the 13.
	return code == codeVal || code == fullLabel || code == network
}
