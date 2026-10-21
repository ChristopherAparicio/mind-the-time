package app

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"mind-the-time/internal/domain"
)

// EvaluationOverrides carries the one-off adjustments a user can request for a
// single evaluation without touching the stored commute. Empty fields keep the
// stored value.
type EvaluationOverrides struct {
	MaximumInitialWalkMinutes  string
	SaintLazareTransferMinutes string
	SelectedCombination        string
}

func (o EvaluationOverrides) isZero() bool {
	return strings.TrimSpace(o.MaximumInitialWalkMinutes) == "" &&
		strings.TrimSpace(o.SaintLazareTransferMinutes) == "" &&
		strings.TrimSpace(o.SelectedCombination) == ""
}

// CommuteForEvaluation returns the commute to evaluate once the overrides are
// applied. A non-empty override that does not parse is a *ValidationError, not
// a silent fallback to the stored value.
func CommuteForEvaluation(base domain.Commute, overrides EvaluationOverrides) (domain.Commute, error) {
	if overrides.isZero() {
		return base, nil
	}
	initialWalk := base.MaximumInitialWalk()
	if strings.TrimSpace(overrides.MaximumInitialWalkMinutes) != "" {
		parsed, err := parseOverrideBufferMinutes(overrides.MaximumInitialWalkMinutes, FieldMaximumInitialWalk)
		if err != nil {
			return domain.Commute{}, err
		}
		initialWalk = parsed
	}
	saintLazareTransfer := base.SaintLazareMinimumTransferTime()
	if strings.TrimSpace(overrides.SaintLazareTransferMinutes) != "" {
		parsed, err := parseOverrideBufferMinutes(overrides.SaintLazareTransferMinutes, FieldSaintLazareTransfer)
		if err != nil {
			return domain.Commute{}, err
		}
		saintLazareTransfer = parsed
	}
	preferredLines := base.PreferredLines()
	if selected := strings.TrimSpace(overrides.SelectedCombination); selected != "" {
		preferredLines = domain.NewPreferredLines(selected)
	}
	commute, err := domain.NewCommute(base.Origin(), base.Destination(), base.Schedule(), initialWalk, saintLazareTransfer, preferredLines)
	if err != nil {
		return domain.Commute{}, &ValidationError{Field: FieldCommute, Err: err}
	}
	return commute, nil
}

// Unlike the creation form, an override accepts zero: it means "evaluate
// without this constraint".
func parseOverrideBufferMinutes(input string, field ValidationField) (domain.TransferBuffer, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || parsed < 0 {
		return domain.TransferBuffer{}, &ValidationError{Field: field, Err: errors.New("must be a non-negative number of minutes")}
	}
	buf, err := domain.NewTransferBuffer(time.Duration(parsed) * time.Minute)
	if err != nil {
		return domain.TransferBuffer{}, &ValidationError{Field: field, Err: err}
	}
	return buf, nil
}
