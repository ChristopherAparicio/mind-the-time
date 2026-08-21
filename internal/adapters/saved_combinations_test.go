package adapters

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

func TestMemoryCommutesReplaceSavedPatternsByProviderIdentity(t *testing.T) {
	owners := NewMemoryOwners()
	commutes, err := NewMemoryCommutes(owners)
	if err != nil {
		t.Fatal(err)
	}
	owner := mustCreateOwner(t, owners)
	record := mustCreateCommute(t, commutes, owner, "Office", "Paris")
	first := savedTestPattern(t, "line:branch-a")
	second := savedTestPattern(t, "line:branch-b")

	record, err = commutes.ReplaceSavedRouteCombinations(context.Background(), owner, record.ID, []domain.RoutePattern{first, second, first})
	if err != nil {
		t.Fatalf("ReplaceSavedRouteCombinations() error = %v", err)
	}
	if got, want := len(record.SavedCombinations), 2; got != want {
		t.Fatalf("saved combinations = %d, want %d for same-label distinct provider patterns", got, want)
	}
	ids := savedCombinationIDs(record.SavedCombinations)

	replacement, err := commutes.ReplaceSavedRouteCombinations(context.Background(), owner, record.ID, []domain.RoutePattern{second, first})
	if err != nil {
		t.Fatalf("ReplaceSavedRouteCombinations() error = %v", err)
	}
	for _, saved := range replacement.SavedCombinations {
		if got, want := ids[saved.Pattern.Fingerprint()], saved.ID; got != want {
			t.Fatalf("pattern %q ID = %q, want preserved %q", saved.Pattern.Fingerprint(), got, want)
		}
	}
	replacement.SavedCombinations[0] = app.SavedRouteCombination{}
	stored, err := commutes.Get(context.Background(), owner, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.SavedCombinations) != 2 || stored.SavedCombinations[0].ID == "" {
		t.Fatalf("stored saved combinations were mutated through returned record: %#v", stored.SavedCombinations)
	}
}

func TestFileStoreMigratesV1AndRoundTripsSavedPatternsWithoutRuntimeFields(t *testing.T) {
	path := t.TempDir() + "/owners.json"
	legacy := `{"version":1,"owners":[{"id":"VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk","expiresAt":"2099-01-01T00:00:00Z","commutes":[{"id":"gUKh-4ohkm4jqGcNkVqc1gifDqBeZqeYE-kyS2L361w","name":"Retour","origin":"Home","destination":"Paris","activeDays":[1],"departureTime":"18:00","maximumInitialWalk":300000000000,"saintLazareMinimumTransferTime":420000000000}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(FileStoreSettings{Path: path})
	if err != nil {
		t.Fatalf("NewFileStore(v1) error = %v", err)
	}
	owner := app.OwnerID("VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk")
	commuteID := app.CommuteID("gUKh-4ohkm4jqGcNkVqc1gifDqBeZqeYE-kyS2L361w")
	first := savedTestPattern(t, "line:branch-a")
	second := savedTestPattern(t, "line:branch-b")
	record, err := store.Commutes().ReplaceSavedRouteCombinations(context.Background(), owner, commuteID, []domain.RoutePattern{first, second})
	if err != nil {
		t.Fatalf("ReplaceSavedRouteCombinations(v1) error = %v", err)
	}
	ids := savedCombinationIDs(record.SavedCombinations)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot["version"], float64(fileStoreVersion); got != want {
		t.Fatalf("snapshot version = %v, want %v", got, want)
	}
	if containsRuntimeSnapshotField(snapshot) {
		t.Fatalf("saved combination snapshot contains a runtime date or window: %s", data)
	}

	store, err = NewFileStore(FileStoreSettings{Path: path})
	if err != nil {
		t.Fatalf("NewFileStore(v2) error = %v", err)
	}
	record, err = store.Commutes().ReplaceSavedRouteCombinations(context.Background(), owner, commuteID, []domain.RoutePattern{second, first})
	if err != nil {
		t.Fatalf("ReplaceSavedRouteCombinations(v2) error = %v", err)
	}
	for _, saved := range record.SavedCombinations {
		if got, want := ids[saved.Pattern.Fingerprint()], saved.ID; got != want {
			t.Fatalf("pattern %q ID = %q, want restart-stable %q", saved.Pattern.Fingerprint(), got, want)
		}
	}
}

func TestFileStoreRoundTripsSavedPatternEndpointNames(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	record := mustCreateCommute(t, store.Commutes(), owner, "Office", "Paris")
	pattern := savedTestPattern(t, "line:branch-a")
	record = mustSaveTestPatterns(t, store.Commutes(), owner, record.ID, []domain.RoutePattern{pattern})

	store = mustNewFileStore(t, settings)
	record, err := store.Commutes().Get(context.Background(), owner, record.ID)
	if err != nil {
		t.Fatalf("Get() after restart error = %v", err)
	}
	leg := record.SavedCombinations[0].Pattern.Legs()[0]
	if leg.BoardName() != "Origin Station" || leg.AlightName() != "Destination Station" {
		t.Fatalf("saved endpoint names = %q to %q, want Origin Station to Destination Station", leg.BoardName(), leg.AlightName())
	}
}

func TestFileStoreLoadsV2SavedPatternWithoutEndpointNames(t *testing.T) {
	ownerID, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	commuteID, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	combinationID, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := fileSnapshot{
		Version: fileStoreVersion,
		Owners: []fileOwner{{
			ID:        ownerID,
			ExpiresAt: time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC),
			Commutes: []fileCommute{{
				ID:                 commuteID,
				Name:               "Office",
				Origin:             "Home",
				Destination:        "Paris",
				ActiveDays:         []int{int(time.Monday)},
				DepartureTime:      "08:30",
				AccessToFirstBoard: int64(5 * time.Minute),
				Interchange:        int64(5 * time.Minute),
				SavedCombinations: []fileSavedRouteCombination{{
					ID: combinationID,
					Legs: []fileRoutePatternLeg{{
						Line:     "line:branch-a",
						BoardAt:  "stop:origin",
						AlightAt: "stop:destination",
						Network:  "Train",
						Code:     "L",
					}},
				}},
			}},
		}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/owners.json"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(FileStoreSettings{Path: path})
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	record, err := store.Commutes().Get(context.Background(), app.OwnerID(ownerID), app.CommuteID(commuteID))
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	leg := record.SavedCombinations[0].Pattern.Legs()[0]
	if leg.BoardName() != "" || leg.AlightName() != "" {
		t.Fatalf("legacy endpoint names = %q to %q, want blank values", leg.BoardName(), leg.AlightName())
	}
}

func TestMemoryCommutesUpdateRetainsSavedPatternsUnlessAnEndpointChanges(t *testing.T) {
	owners := NewMemoryOwners()
	commutes, err := NewMemoryCommutes(owners)
	if err != nil {
		t.Fatal(err)
	}
	owner := mustCreateOwner(t, owners)
	record := mustCreateCommute(t, commutes, owner, "Office", "Paris")
	patterns := []domain.RoutePattern{savedTestPattern(t, "line:branch-a"), savedTestPattern(t, "line:branch-b")}
	record = mustSaveTestPatterns(t, commutes, owner, record.ID, patterns)
	ids := savedCombinationIDs(record.SavedCombinations)

	record = mustUpdateSavedTestCommute(t, commutes, owner, record.ID, "Renamed", savedTestCommute(t, "Home", "Paris", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	assertSavedCombinationIDs(t, record.SavedCombinations, ids)

	record = mustUpdateSavedTestCommute(t, commutes, owner, record.ID, "Renamed", savedTestCommute(t, "Elsewhere", "Paris", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	if len(record.SavedCombinations) != 0 {
		t.Fatalf("origin update retained stale saved combinations: %#v", record.SavedCombinations)
	}

	record = mustSaveTestPatterns(t, commutes, owner, record.ID, patterns)
	record = mustUpdateSavedTestCommute(t, commutes, owner, record.ID, "Renamed", savedTestCommute(t, "Elsewhere", "Lyon", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	if len(record.SavedCombinations) != 0 {
		t.Fatalf("destination update retained stale saved combinations: %#v", record.SavedCombinations)
	}
}

func TestFileStoreUpdateRetainsSavedPatternsUnlessAnEndpointChangesAndPersistsClearing(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	record := mustCreateCommute(t, store.Commutes(), owner, "Office", "Paris")
	patterns := []domain.RoutePattern{savedTestPattern(t, "line:branch-a"), savedTestPattern(t, "line:branch-b")}
	record = mustSaveTestPatterns(t, store.Commutes(), owner, record.ID, patterns)
	ids := savedCombinationIDs(record.SavedCombinations)

	record = mustUpdateSavedTestCommute(t, store.Commutes(), owner, record.ID, "Renamed", savedTestCommute(t, "Home", "Paris", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	assertSavedCombinationIDs(t, record.SavedCombinations, ids)

	record = mustUpdateSavedTestCommute(t, store.Commutes(), owner, record.ID, "Renamed", savedTestCommute(t, "Elsewhere", "Paris", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	if len(record.SavedCombinations) != 0 {
		t.Fatalf("origin update retained stale saved combinations: %#v", record.SavedCombinations)
	}

	record = mustSaveTestPatterns(t, store.Commutes(), owner, record.ID, patterns)
	record = mustUpdateSavedTestCommute(t, store.Commutes(), owner, record.ID, "Renamed", savedTestCommute(t, "Elsewhere", "Lyon", "09:45", []time.Weekday{time.Tuesday}, 7*time.Minute, 8*time.Minute))
	if len(record.SavedCombinations) != 0 {
		t.Fatalf("destination update retained stale saved combinations: %#v", record.SavedCombinations)
	}

	store = mustNewFileStore(t, settings)
	restarted, err := store.Commutes().Get(context.Background(), owner, record.ID)
	if err != nil {
		t.Fatalf("Get() after restart error = %v", err)
	}
	if len(restarted.SavedCombinations) != 0 {
		t.Fatalf("restart restored cleared saved combinations: %#v", restarted.SavedCombinations)
	}
}

func savedTestPattern(t *testing.T, line string) domain.RoutePattern {
	t.Helper()
	leg, err := domain.NewPatternLegWithNames(domain.ProviderLineID(line), "stop:origin", "stop:destination", "Origin Station", "Destination Station", domain.NewTransitLabel("Train", "L"))
	if err != nil {
		t.Fatal(err)
	}
	pattern, err := domain.NewRoutePattern([]domain.PatternLeg{leg})
	if err != nil {
		t.Fatal(err)
	}
	return pattern
}

func savedCombinationIDs(combinations []app.SavedRouteCombination) map[string]app.SavedCombinationID {
	ids := make(map[string]app.SavedCombinationID, len(combinations))
	for _, saved := range combinations {
		ids[saved.Pattern.Fingerprint()] = saved.ID
	}
	return ids
}

func mustSaveTestPatterns(t *testing.T, commutes app.CommuteStore, owner app.OwnerID, commuteID app.CommuteID, patterns []domain.RoutePattern) app.CommuteRecord {
	t.Helper()
	record, err := commutes.ReplaceSavedRouteCombinations(context.Background(), owner, commuteID, patterns)
	if err != nil {
		t.Fatalf("ReplaceSavedRouteCombinations() error = %v", err)
	}
	return record
}

func mustUpdateSavedTestCommute(t *testing.T, commutes app.CommuteStore, owner app.OwnerID, commuteID app.CommuteID, name string, commute domain.Commute) app.CommuteRecord {
	t.Helper()
	record, err := commutes.Update(context.Background(), owner, commuteID, mustCommuteName(t, name), commute)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	return record
}

func savedTestCommute(t *testing.T, origin, destination, departure string, weekdays []time.Weekday, access, interchange time.Duration) domain.Commute {
	t.Helper()
	originPlace, err := domain.NewPlace(origin)
	if err != nil {
		t.Fatal(err)
	}
	destinationPlace, err := domain.NewPlace(destination)
	if err != nil {
		t.Fatal(err)
	}
	departureTime, err := domain.NewDepartureTime(departure)
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := domain.NewWeeklySchedule(weekdays, departureTime)
	if err != nil {
		t.Fatal(err)
	}
	accessBuffer, err := domain.NewTransferBuffer(access)
	if err != nil {
		t.Fatal(err)
	}
	interchangeBuffer, err := domain.NewTransferBuffer(interchange)
	if err != nil {
		t.Fatal(err)
	}
	commute, err := domain.NewCommute(originPlace, destinationPlace, schedule, accessBuffer, interchangeBuffer, domain.NewPreferredLines(""))
	if err != nil {
		t.Fatal(err)
	}
	return commute
}

func assertSavedCombinationIDs(t *testing.T, combinations []app.SavedRouteCombination, want map[string]app.SavedCombinationID) {
	t.Helper()
	if len(combinations) != len(want) {
		t.Fatalf("saved combinations = %d, want %d", len(combinations), len(want))
	}
	for _, combination := range combinations {
		if got := want[combination.Pattern.Fingerprint()]; got != combination.ID {
			t.Fatalf("pattern %q ID = %q, want %q", combination.Pattern.Fingerprint(), combination.ID, got)
		}
	}
}

func containsRuntimeSnapshotField(snapshot map[string]any) bool {
	encoded, _ := json.Marshal(snapshot)
	for _, field := range []string{"earliest", "window", "departure", "date"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			return true
		}
	}
	return false
}
