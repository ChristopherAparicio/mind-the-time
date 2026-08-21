package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mind-the-time/internal/domain"
)

var ErrInvalidInput = errors.New("invalid input")

type ValidationField string

const (
	FieldName                ValidationField = "name"
	FieldOrigin              ValidationField = "origin"
	FieldDestination         ValidationField = "destination"
	FieldDepartureTime       ValidationField = "departure time"
	FieldWeekdays            ValidationField = "weekdays"
	FieldMaximumInitialWalk  ValidationField = "maximum initial walk"
	FieldSaintLazareTransfer ValidationField = "saint lazare transfer"
	FieldCommute             ValidationField = "commute"
)

type ValidationError struct {
	Field ValidationField
	Err   error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %v", e.Field, e.Err)
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

func (e *ValidationError) Is(target error) bool {
	return target == ErrInvalidInput || errors.Is(e.Err, target)
}

type CommuteInput struct {
	Name                       string
	Origin                     string
	Destination                string
	DepartureTime              string
	Weekdays                   []time.Weekday
	MaximumInitialWalkMinutes  string
	SaintLazareTransferMinutes string
	PreferredLines             string
}

func NewCommute(input CommuteInput) (domain.CommuteName, domain.Commute, error) {
	name, err := domain.NewCommuteName(input.Name)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldName, Err: err}
	}
	origin, err := domain.NewPlace(input.Origin)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldOrigin, Err: err}
	}
	destination, err := domain.NewPlace(input.Destination)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldDestination, Err: err}
	}
	departureTime, err := domain.NewDepartureTime(input.DepartureTime)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldDepartureTime, Err: err}
	}
	schedule, err := domain.NewWeeklySchedule(input.Weekdays, departureTime)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldWeekdays, Err: err}
	}
	initialWalk, err := parseBufferMinutes(input.MaximumInitialWalkMinutes, FieldMaximumInitialWalk)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, err
	}
	saintLazareTransfer, err := prepareSaintLazareTransfer(input.SaintLazareTransferMinutes, FieldSaintLazareTransfer)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, err
	}
	preferredLines := domain.NewPreferredLines(input.PreferredLines)
	commute, err := domain.NewCommute(origin, destination, schedule, initialWalk, saintLazareTransfer, preferredLines)
	if err != nil {
		return domain.CommuteName{}, domain.Commute{}, &ValidationError{Field: FieldCommute, Err: err}
	}
	return name, commute, nil
}

func parseBufferMinutes(input string, field ValidationField) (domain.TransferBuffer, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return domain.TransferBuffer{}, &ValidationError{Field: field, Err: errors.New("must be a positive number of minutes")}
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		return domain.TransferBuffer{}, &ValidationError{Field: field, Err: errors.New("must be a positive number of minutes")}
	}
	buf, err := domain.NewTransferBuffer(time.Duration(parsed) * time.Minute)
	if err != nil {
		return domain.TransferBuffer{}, &ValidationError{Field: field, Err: err}
	}
	return buf, nil
}

func prepareSaintLazareTransfer(input string, field ValidationField) (domain.TransferBuffer, error) {
	return parseBufferMinutes(input, field)
}
