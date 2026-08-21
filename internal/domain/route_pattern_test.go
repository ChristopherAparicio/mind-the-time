package domain

import (
	"errors"
	"testing"
)

func TestRoutePatternIdentityUsesProviderIDsNotDisplayLabels(t *testing.T) {
	label := NewTransitLabel("Train", "L")
	firstLeg, err := NewPatternLeg("line:branch-a", "stop:origin", "stop:destination", label)
	if err != nil {
		t.Fatal(err)
	}
	secondLeg, err := NewPatternLeg("line:branch-b", "stop:origin", "stop:destination", label)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewRoutePattern([]PatternLeg{firstLeg})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRoutePattern([]PatternLeg{secondLeg})
	if err != nil {
		t.Fatal(err)
	}

	if first.Equal(second) {
		t.Fatal("patterns with different provider line IDs merged because their labels matched")
	}
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("patterns with different provider line IDs have the same fingerprint")
	}
	if got, want := first.Combination(), second.Combination(); got != want {
		t.Fatalf("display labels changed unexpectedly: %q != %q", got, want)
	}
}

func TestRoutePatternRejectsMissingProviderIdentity(t *testing.T) {
	_, err := NewPatternLeg("", "stop:origin", "stop:destination", NewTransitLabel("Train", "L"))
	if !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("NewPatternLeg() error = %v, want ErrInvalidRoutePattern", err)
	}
}

func TestRoutePatternDisplayNamesDoNotAffectProviderIdentity(t *testing.T) {
	firstLeg, err := NewPatternLegWithNames("line:branch-a", "stop:origin", "stop:destination", "Origin A", "Destination A", NewTransitLabel("Train", "L"))
	if err != nil {
		t.Fatal(err)
	}
	secondLeg, err := NewPatternLegWithNames("line:branch-a", "stop:origin", "stop:destination", "Origin B", "Destination B", NewTransitLabel("Different", "Label"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewRoutePattern([]PatternLeg{firstLeg})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRoutePattern([]PatternLeg{secondLeg})
	if err != nil {
		t.Fatal(err)
	}

	if !first.Equal(second) || first.Fingerprint() != second.Fingerprint() {
		t.Fatal("display names or labels changed provider-ID route identity")
	}
	leg := first.Legs()[0]
	if got, want := leg.BoardName(), "Origin A"; got != want {
		t.Fatalf("BoardName() = %q, want %q", got, want)
	}
	if got, want := leg.AlightName(), "Destination A"; got != want {
		t.Fatalf("AlightName() = %q, want %q", got, want)
	}
}
