package adapters

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

func TestMemoryOwnersTouchExpiryAndDelete(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	owners := NewMemoryOwners(MemoryRetention{MaxEntries: 1, TTL: time.Hour, Now: clock.Now})
	owner, err := owners.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(59 * time.Minute)
	if renewed, err := owners.Touch(context.Background(), owner); err != nil || !renewed {
		t.Fatalf("Touch() = %v, %v, want true, nil", renewed, err)
	}
	clock.Advance(2 * time.Minute)
	if exists, err := owners.Exists(context.Background(), owner); err != nil || !exists {
		t.Fatalf("Exists() = %v, %v, want true, nil", exists, err)
	}
	if _, err := owners.Create(context.Background()); !errors.Is(err, app.ErrOwnerCapacityExceeded) {
		t.Fatalf("Create() error = %v, want ErrOwnerCapacityExceeded", err)
	}
	if err := owners.Delete(context.Background(), owner); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if exists, err := owners.Exists(context.Background(), owner); err != nil || exists {
		t.Fatalf("Exists() after Delete() = %v, %v, want false, nil", exists, err)
	}
	if renewed, err := owners.Touch(context.Background(), owner); err != nil || renewed {
		t.Fatalf("Touch(deleted) = %v, %v, want false, nil", renewed, err)
	}
}

func TestMemoryOwnersTouchDoesNotRenewExpiredOwner(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	owners := NewMemoryOwners(MemoryRetention{MaxEntries: 1, TTL: time.Hour, Now: clock.Now})
	owner := mustCreateOwner(t, owners)
	clock.Advance(time.Hour)

	if renewed, err := owners.Touch(context.Background(), owner); err != nil || renewed {
		t.Fatalf("Touch(expired) = %v, %v, want false, nil", renewed, err)
	}
}

func TestMemoryCommutesNamedCRUDAndIsolation(t *testing.T) {
	owners := NewMemoryOwners(MemoryRetention{MaxEntries: 2, TTL: time.Hour})
	commutes, err := NewMemoryCommutes(owners, MemoryRetention{MaxEntries: 3, TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	firstOwner := mustCreateOwner(t, owners)
	secondOwner := mustCreateOwner(t, owners)

	first := mustCreateCommute(t, commutes, firstOwner, "Office", "Paris")
	second := mustCreateCommute(t, commutes, firstOwner, "Gym", "Versailles")
	list, err := commutes.List(context.Background(), firstOwner)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("List() = %#v, want insertion order [%q, %q]", list, first.ID, second.ID)
	}
	if _, err := commutes.Get(context.Background(), secondOwner, first.ID); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Get() by another owner error = %v, want ErrCommuteNotFound", err)
	}
	updated := mustUpdateCommute(t, commutes, firstOwner, first.ID, "HQ", "Lyon")
	if updated.Name.Text() != "HQ" || updated.Commute.Destination().Text() != "Lyon" {
		t.Fatalf("Update() = %#v, want updated named commute", updated)
	}
	if err := commutes.Delete(context.Background(), firstOwner, second.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := commutes.Get(context.Background(), firstOwner, second.ID); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrCommuteNotFound", err)
	}
}

func TestMemoryCommutesRespectOwnerExpiry(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC))
	owners := NewMemoryOwners(MemoryRetention{MaxEntries: 1, TTL: time.Minute, Now: clock.Now})
	commutes, err := NewMemoryCommutes(owners, MemoryRetention{MaxEntries: 1, TTL: time.Hour, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	owner := mustCreateOwner(t, owners)
	record := mustCreateCommute(t, commutes, owner, "Office", "Paris")
	clock.Advance(time.Minute)
	if _, err := commutes.Get(context.Background(), owner, record.ID); !errors.Is(err, app.ErrCommuteNotFound) {
		t.Fatalf("Get(expired owner) error = %v, want ErrCommuteNotFound", err)
	}
}

func TestMemoryStoresSupportConcurrentAccess(t *testing.T) {
	const requests = 32
	owners := NewMemoryOwners(MemoryRetention{MaxEntries: 1, TTL: time.Hour})
	commutes, err := NewMemoryCommutes(owners, MemoryRetention{MaxEntries: requests, TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewMemoryCommutes() error = %v", err)
	}
	owner := mustCreateOwner(t, owners)

	errs := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := commutes.Create(context.Background(), owner, mustCommuteName(t, "Office"), mustCommute(t, "Paris"))
			if err != nil {
				errs <- err
				return
			}
			if _, err := commutes.Get(context.Background(), owner, record.ID); err != nil {
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

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func mustCreateOwner(t *testing.T, owners app.OwnerStore) app.OwnerID {
	t.Helper()
	owner, err := owners.Create(context.Background())
	if err != nil {
		t.Fatalf("Create(owner) error = %v", err)
	}
	return owner
}

func mustCreateCommute(t *testing.T, commutes app.CommuteStore, owner app.OwnerID, name, destination string) app.CommuteRecord {
	t.Helper()
	record, err := commutes.Create(context.Background(), owner, mustCommuteName(t, name), mustCommute(t, destination))
	if err != nil {
		t.Fatalf("Create(commute) error = %v", err)
	}
	return record
}

func mustUpdateCommute(t *testing.T, commutes app.CommuteStore, owner app.OwnerID, id app.CommuteID, name, destination string) app.CommuteRecord {
	t.Helper()
	record, err := commutes.Update(context.Background(), owner, id, mustCommuteName(t, name), mustCommute(t, destination))
	if err != nil {
		t.Fatalf("Update(commute) error = %v", err)
	}
	return record
}

func mustCommuteName(t *testing.T, text string) domain.CommuteName {
	t.Helper()
	name, err := domain.NewCommuteName(text)
	if err != nil {
		t.Fatalf("NewCommuteName() error = %v", err)
	}
	return name
}

func mustCommute(t *testing.T, destination string) domain.Commute {
	t.Helper()
	origin, err := domain.NewPlace("Home")
	if err != nil {
		t.Fatal(err)
	}
	destinationPlace, err := domain.NewPlace(destination)
	if err != nil {
		t.Fatal(err)
	}
	departure, err := domain.NewDepartureTime("08:30")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := domain.NewWeeklySchedule([]time.Weekday{time.Monday}, departure)
	if err != nil {
		t.Fatal(err)
	}
	buffer, err := domain.NewTransferBuffer(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	commute, err := domain.NewCommute(origin, destinationPlace, schedule, buffer, buffer, domain.NewPreferredLines(""))
	if err != nil {
		t.Fatal(err)
	}
	return commute
}
