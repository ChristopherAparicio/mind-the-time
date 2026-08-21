package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mind-the-time/internal/domain"
)

func TestValidCommuteInputBuildsEditableWeekdayCommute(t *testing.T) {
	input := CommuteInput{
		Name:                       "Retour à la maison",
		Origin:                     "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "7",
		Weekdays: []time.Weekday{
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
		},
	}

	if input.Origin != "Café Rivoli (10 Rue de Rivoli, 75004 Paris)" {
		t.Errorf("Origin = %q", input.Origin)
	}
	if input.Name != "Retour à la maison" {
		t.Errorf("Name = %q", input.Name)
	}
	if input.Destination != "Cergy-le-Haut" {
		t.Errorf("Destination = %q", input.Destination)
	}
	if input.DepartureTime != "18:00" {
		t.Errorf("DepartureTime = %q", input.DepartureTime)
	}
	if len(input.Weekdays) != 5 || input.Weekdays[0] != time.Monday || input.Weekdays[4] != time.Friday {
		t.Errorf("Weekdays = %v, want Monday through Friday", input.Weekdays)
	}

	name, commute, err := NewCommute(input)
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	if got, want := name.Text(), "Retour à la maison"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := commute.MaximumInitialWalk().Duration(), 5*time.Minute; got != want {
		t.Errorf("MaximumInitialWalk = %v, want %v", got, want)
	}
	if got, want := commute.SaintLazareMinimumTransferTime().Duration(), 7*time.Minute; got != want {
		t.Errorf("SaintLazareMinimumTransferTime = %v, want %v", got, want)
	}
}

func TestNewCommuteRejectsInvalidName(t *testing.T) {
	input := CommuteInput{
		Name:                       " \t",
		Origin:                     "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "7",
		Weekdays: []time.Weekday{
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
		},
	}

	_, _, err := NewCommute(input)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("NewCommute() error = %v, want ErrInvalidInput", err)
	}
}

func TestValidationErrorCompatibility(t *testing.T) {
	input := CommuteInput{
		Name:                       " \t",
		Origin:                     "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "7",
		Weekdays: []time.Weekday{
			time.Monday,
		},
	}

	_, _, err := NewCommute(input)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("errors.Is(err, ErrInvalidInput) = false, want true")
	}

	if !errors.Is(err, domain.ErrInvalidCommuteName) {
		t.Errorf("errors.Is(err, domain.ErrInvalidCommuteName) = false, want true")
	}

	var valErr *ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("errors.As(err, &valErr) = false, want true")
	}
	if valErr.Field != FieldName {
		t.Errorf("valErr.Field = %q, want %q", valErr.Field, FieldName)
	}
}

func TestInputFromCommuteRecordIncludesPersistedName(t *testing.T) {
	input := CommuteInput{
		Name:                       "Retour à la maison",
		Origin:                     "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:00",
		MaximumInitialWalkMinutes:  "5",
		SaintLazareTransferMinutes: "7",
		Weekdays: []time.Weekday{
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
		},
	}
	name, commute, err := NewCommute(input)
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}

	inputRecord := InputFromCommuteRecord(CommuteRecord{Name: name, Commute: commute})
	if got, want := inputRecord.Name, "Retour à la maison"; got != want {
		t.Errorf("InputFromCommuteRecord().Name = %q, want %q", got, want)
	}
}

func TestNewCommuteRejectsInvalidBuffers(t *testing.T) {
	baseInput := CommuteInput{
		Name:          "Retour à la maison",
		Origin:        "Café Rivoli (10 Rue de Rivoli, 75004 Paris)",
		Destination:   "Cergy-le-Haut",
		DepartureTime: "18:00",
		Weekdays:      []time.Weekday{time.Monday},
	}

	invalidValues := []string{"", "   ", "0", "-1", "abc"}
	for _, val := range invalidValues {
		t.Run("walk_"+val, func(t *testing.T) {
			input := baseInput
			input.MaximumInitialWalkMinutes = val
			input.SaintLazareTransferMinutes = "5"
			_, _, err := NewCommute(input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput for walk=%q, got %v", val, err)
			}
			var valErr *ValidationError
			if !errors.As(err, &valErr) || valErr.Field != FieldMaximumInitialWalk {
				t.Errorf("expected ValidationError on FieldMaximumInitialWalk, got %v", err)
			}
		})

		t.Run("transfer_"+val, func(t *testing.T) {
			input := baseInput
			input.MaximumInitialWalkMinutes = "5"
			input.SaintLazareTransferMinutes = val
			_, _, err := NewCommute(input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput for transfer=%q, got %v", val, err)
			}
			var valErr *ValidationError
			if !errors.As(err, &valErr) || valErr.Field != FieldSaintLazareTransfer {
				t.Errorf("expected ValidationError on FieldSaintLazareTransfer, got %v", err)
			}
		})
	}
}

func TestCustomBuffersAndPreferredLinesInput(t *testing.T) {
	input := CommuteInput{
		Name:                       "Trajet sur mesure",
		Origin:                     "10 Rue de Rivoli, Paris",
		Destination:                "Cergy-le-Haut",
		DepartureTime:              "18:15",
		Weekdays:                   []time.Weekday{time.Monday},
		MaximumInitialWalkMinutes:  "8",
		SaintLazareTransferMinutes: "10",
		PreferredLines:             "Métro 3, RER A",
	}

	name, commute, err := NewCommute(input)
	if err != nil {
		t.Fatalf("NewCommute() error = %v", err)
	}
	if got, want := name.Text(), "Trajet sur mesure"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := commute.MaximumInitialWalk().Duration(), 8*time.Minute; got != want {
		t.Errorf("MaximumInitialWalk = %v, want %v", got, want)
	}
	if got, want := commute.SaintLazareMinimumTransferTime().Duration(), 10*time.Minute; got != want {
		t.Errorf("SaintLazareMinimumTransferTime = %v, want %v", got, want)
	}
	if got, want := commute.PreferredLines().Raw(), "Métro 3, RER A"; got != want {
		t.Errorf("PreferredLines().Raw() = %q, want %q", got, want)
	}

	exportedInput := InputFromCommuteRecord(CommuteRecord{Name: name, Commute: commute})
	if got, want := exportedInput.MaximumInitialWalkMinutes, "8"; got != want {
		t.Errorf("exported MaximumInitialWalkMinutes = %q, want %q", got, want)
	}
	if got, want := exportedInput.SaintLazareTransferMinutes, "10"; got != want {
		t.Errorf("exported SaintLazareTransferMinutes = %q, want %q", got, want)
	}
	if got, want := exportedInput.PreferredLines, "Métro 3, RER A"; got != want {
		t.Errorf("exported PreferredLines = %q, want %q", got, want)
	}
}

var _ OwnerStore = ownerStoreContract{}

type ownerStoreContract struct{}

func (ownerStoreContract) Create(context.Context) (OwnerID, error) { return "", nil }
func (ownerStoreContract) Touch(context.Context, OwnerID) (bool, error) {
	return false, nil
}
func (ownerStoreContract) Exists(context.Context, OwnerID) (bool, error) {
	return false, nil
}
func (ownerStoreContract) Delete(context.Context, OwnerID) error { return nil }

var _ CommuteStore = commuteStoreContract{}

type commuteStoreContract struct{}

func (commuteStoreContract) List(context.Context, OwnerID) ([]CommuteRecord, error) {
	return nil, nil
}
func (commuteStoreContract) Create(context.Context, OwnerID, domain.CommuteName, domain.Commute) (CommuteRecord, error) {
	return CommuteRecord{}, nil
}
func (commuteStoreContract) Get(context.Context, OwnerID, CommuteID) (CommuteRecord, error) {
	return CommuteRecord{}, nil
}
func (commuteStoreContract) Update(context.Context, OwnerID, CommuteID, domain.CommuteName, domain.Commute) (CommuteRecord, error) {
	return CommuteRecord{}, nil
}
func (commuteStoreContract) Delete(context.Context, OwnerID, CommuteID) error { return nil }
func (commuteStoreContract) ReplaceSavedRouteCombinations(context.Context, OwnerID, CommuteID, []domain.RoutePattern) (CommuteRecord, error) {
	return CommuteRecord{}, nil
}
