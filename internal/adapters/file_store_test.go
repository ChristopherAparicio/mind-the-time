package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mind-the-time/internal/app"
)

func TestFileStorePersistsNamedCommutesAcrossRestart(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	first := mustCreateCommute(t, store.Commutes(), owner, "Office", "Paris")
	second := mustCreateCommute(t, store.Commutes(), owner, "Gym", "Versailles")

	store = mustNewFileStore(t, settings)
	if exists, err := store.Owners().Exists(context.Background(), owner); err != nil || !exists {
		t.Fatalf("Exists() after restart = %v, %v, want true, nil", exists, err)
	}
	list, err := store.Commutes().List(context.Background(), owner)
	if err != nil {
		t.Fatalf("List() after restart error = %v", err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("List() after restart = %#v, want ordered records", list)
	}
}

func TestFileStoreTouchExpiryAndOwnerDelete(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	settings.OwnerTTL = time.Hour
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	record := mustCreateCommute(t, store.Commutes(), owner, "Office", "Paris")
	clock.Advance(59 * time.Minute)
	if renewed, err := store.Owners().Touch(context.Background(), owner); err != nil || !renewed {
		t.Fatalf("Touch() = %v, %v, want true, nil", renewed, err)
	}
	clock.Advance(2 * time.Minute)
	if exists, err := store.Owners().Exists(context.Background(), owner); err != nil || !exists {
		t.Fatalf("Exists() after Touch() = %v, %v, want true, nil", exists, err)
	}
	if err := store.Owners().Delete(context.Background(), owner); err != nil {
		t.Fatalf("Delete(owner) error = %v", err)
	}
	if _, err := store.Commutes().Get(context.Background(), owner, record.ID); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Get() after owner delete error = %v, want ErrCommuteNotFound", err)
	}
	if renewed, err := store.Owners().Touch(context.Background(), owner); err != nil || renewed {
		t.Fatalf("Touch(deleted) = %v, %v, want false, nil", renewed, err)
	}
}

func TestFileStoreTouchDoesNotRenewExpiredOwner(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	settings.OwnerTTL = time.Hour
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	clock.Advance(time.Hour)

	if renewed, err := store.Owners().Touch(context.Background(), owner); err != nil || renewed {
		t.Fatalf("Touch(expired) = %v, %v, want false, nil", renewed, err)
	}
}

func TestFileStoreOwnerIsolationAndCapacities(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	settings.MaxOwners, settings.MaxTotalCommutes, settings.MaxCommutesPerOwner = 2, 2, 1
	store := mustNewFileStore(t, settings)
	firstOwner := mustCreateOwner(t, store.Owners())
	secondOwner := mustCreateOwner(t, store.Owners())
	if _, err := store.Owners().Create(context.Background()); !errors.Is(err, app.ErrOwnerCapacityExceeded) {
		t.Fatalf("Create(owner) error = %v, want ErrOwnerCapacityExceeded", err)
	}
	first := mustCreateCommute(t, store.Commutes(), firstOwner, "Office", "Paris")
	if _, err := store.Commutes().Create(context.Background(), firstOwner, mustCommuteName(t, "Gym"), mustCommute(t, "Lyon")); !errors.Is(err, app.ErrCommuteCapacityExceeded) {
		t.Fatalf("Create(per-owner capacity) error = %v, want ErrCommuteCapacityExceeded", err)
	}
	if _, err := store.Commutes().Get(context.Background(), secondOwner, first.ID); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Get(other owner) error = %v, want ErrCommuteNotFound", err)
	}
	mustCreateCommute(t, store.Commutes(), secondOwner, "Gym", "Lyon")
	if err := store.Commutes().Delete(context.Background(), secondOwner, "unknown"); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Delete(unknown) error = %v, want ErrCommuteNotFound", err)
	}
}

func TestFileStoreRejectsMalformedAndOversizedSnapshots(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	for name, contents := range map[string][]byte{
		"malformed": []byte(`{"version":1`),
		"trailing":  []byte(`{"version":1,"owners":[]} trailing`),
		"unknown":   []byte(`{"version":3,"owners":[]}`),
		"field":     []byte(`{"version":1,"owners":[],"extra":true}`),
		"oversized": []byte("0123456789"),
	} {
		t.Run(name, func(t *testing.T) {
			settings := testFileStoreSettings(t, clock)
			if name == "oversized" {
				settings.MaxBytes = 4
			}
			if err := os.MkdirAll(filepath.Dir(settings.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings.Path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(settings); err == nil {
				t.Fatal("NewFileStore() error = nil, want strict load error")
			}
			got, err := os.ReadFile(settings.Path)
			if err != nil || string(got) != string(contents) {
				t.Fatalf("invalid snapshot changed: %q, %v", got, err)
			}
		})
	}
}

func TestFileStoreRejectsInvalidSnapshotDataWithoutReplacingIt(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	ownerID, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	commuteID, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	validCommute := fmt.Sprintf(`{"id":%q,"name":"Office","origin":"Home","destination":"Paris","activeDays":[1],"departureTime":"08:30","maximumInitialWalk":300000000000,"saintLazareMinimumTransferTime":300000000000}`, commuteID)
	for name, contents := range map[string]string{
		"invalid owner id": `{"version":1,"owners":[{"id":"bad","expiresAt":"2026-08-04T12:00:00Z","commutes":[]}]}`,
		"invalid time":     fmt.Sprintf(`{"version":1,"owners":[{"id":%q,"expiresAt":"not-a-time","commutes":[]}]}`, ownerID),
		"invalid name":     fmt.Sprintf(`{"version":1,"owners":[{"id":%q,"expiresAt":"2026-08-04T12:00:00Z","commutes":[%s]}]}`, ownerID, strings.Replace(validCommute, `"name":"Office"`, `"name":""`, 1)),
		"invalid commute":  fmt.Sprintf(`{"version":1,"owners":[{"id":%q,"expiresAt":"2026-08-04T12:00:00Z","commutes":[%s]}]}`, ownerID, strings.Replace(validCommute, `"origin":"Home"`, `"origin":""`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			settings := testFileStoreSettings(t, clock)
			if err := os.MkdirAll(filepath.Dir(settings.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings.Path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(settings); err == nil {
				t.Fatal("NewFileStore() error = nil, want validation error")
			}
			got, err := os.ReadFile(settings.Path)
			if err != nil || string(got) != contents {
				t.Fatalf("invalid snapshot changed: %q, %v", got, err)
			}
		})
	}
}

func TestFileStoreCreatesPrivateParentAndSnapshot(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %#o, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %#o, want 0700", dir.Mode().Perm())
	}
}

func TestFileStoreRepairsExistingSnapshotMode(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	before, err := os.ReadFile(settings.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path, 0o644); err != nil {
		t.Fatal(err)
	}

	store = mustNewFileStore(t, settings)

	if exists, err := store.Owners().Exists(context.Background(), owner); err != nil || !exists {
		t.Fatalf("Exists() after mode repair = %v, %v, want true, nil", exists, err)
	}
	after, err := os.ReadFile(settings.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("repaired snapshot changed: %q, %v", after, err)
	}
	info, err := os.Stat(settings.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("repaired snapshot mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestFileStoreDoesNotAdoptMutationWhenWriteFails(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	store := mustNewFileStore(t, testFileStoreSettings(t, clock))
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	store.writeSnapshot = func(fileSnapshot) (bool, error) { return false, errors.New("write failed") }
	if _, err := store.Owners().Create(context.Background()); err == nil {
		t.Fatal("Create() error = nil, want write failure")
	}
	if len(store.owners) != 0 {
		t.Fatalf("failed mutation adopted %d owners", len(store.owners))
	}
	after, err := os.ReadFile(store.path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("canonical snapshot changed: %q, %v", after, err)
	}
}

func TestFileStoreAdoptsCanonicalSnapshotAfterPostRenameError(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	store := mustNewFileStore(t, settings)
	writeSnapshot := store.writeSnapshot
	store.writeSnapshot = func(snapshot fileSnapshot) (bool, error) {
		canonical, err := writeSnapshot(snapshot)
		if err != nil {
			return canonical, err
		}
		return canonical, errors.New("directory durability failed")
	}

	if _, err := store.Owners().Create(context.Background()); err == nil {
		t.Fatal("Create() error = nil, want post-rename durability error")
	}
	if len(store.owners) != 1 {
		t.Fatalf("canonical mutation adopted %d owners, want 1", len(store.owners))
	}
	var owner app.OwnerID
	for owner = range store.owners {
	}

	restarted := mustNewFileStore(t, settings)
	if exists, err := restarted.Owners().Exists(context.Background(), owner); err != nil || !exists {
		t.Fatalf("Exists() after post-rename restart = %v, %v, want true, nil", exists, err)
	}
}

func TestFileStoreSupportsConcurrentAccess(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	settings := testFileStoreSettings(t, clock)
	settings.MaxTotalCommutes, settings.MaxCommutesPerOwner = 64, 64
	store := mustNewFileStore(t, settings)
	owner := mustCreateOwner(t, store.Owners())
	const requests = 32
	errs := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := store.Commutes().Create(context.Background(), owner, mustCommuteName(t, "Office"), mustCommute(t, "Paris"))
			if err == nil {
				_, err = store.Commutes().Get(context.Background(), owner, record.ID)
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent access error = %v", err)
	}
}

func testFileStoreSettings(t *testing.T, clock *manualClock) FileStoreSettings {
	t.Helper()
	return FileStoreSettings{Path: filepath.Join(t.TempDir(), "state", "owners.json"), Now: clock.Now}
}

func mustNewFileStore(t *testing.T, settings FileStoreSettings) *FileStore {
	t.Helper()
	store, err := NewFileStore(settings)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return store
}

func TestFileStoreReadsCommutesWrittenBeforeTheDurationRename(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "store.json")
	legacy := `{"version":1,"owners":[{"id":"VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk","expiresAt":"2099-01-01T00:00:00Z","commutes":[{"id":"gUKh-4ohkm4jqGcNkVqc1gifDqBeZqeYE-kyS2L361w","name":"Retour","origin":"Home","destination":"Paris","activeDays":[1],"departureTime":"18:00","maximumInitialWalk":300000000000,"saintLazareMinimumTransferTime":420000000000}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewFileStore(FileStoreSettings{Path: path})
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	records, err := store.Commutes().List(context.Background(), "VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1; a file written before the rename must still load", len(records))
	}
	if got, want := records[0].Commute.MaximumInitialWalk().Duration(), 5*time.Minute; got != want {
		t.Errorf("access duration = %v, want %v", got, want)
	}
	if got, want := records[0].Commute.SaintLazareMinimumTransferTime().Duration(), 7*time.Minute; got != want {
		t.Errorf("interchange duration = %v, want %v", got, want)
	}
}

func TestFileStoreLoadsZeroValuedStoredRecordsUnchanged(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "store.json")
	legacy := `{"version":1,"owners":[{"id":"VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk","expiresAt":"2099-01-01T00:00:00Z","commutes":[{"id":"gUKh-4ohkm4jqGcNkVqc1gifDqBeZqeYE-kyS2L361w","name":"Retour","origin":"Home","destination":"Paris","activeDays":[1],"departureTime":"18:00","maximumInitialWalk":0,"saintLazareMinimumTransferTime":0}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewFileStore(FileStoreSettings{Path: path})
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	records, err := store.Commutes().List(context.Background(), "VZoUsR8Adcqie14Cau3N08i_HiqwYIKE8kTOw2Qx_mk")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got, want := records[0].Commute.MaximumInitialWalk().Duration(), time.Duration(0); got != want {
		t.Errorf("access duration = %v, want %v", got, want)
	}
	if got, want := records[0].Commute.SaintLazareMinimumTransferTime().Duration(), time.Duration(0); got != want {
		t.Errorf("interchange duration = %v, want %v", got, want)
	}
}
