package adapters

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const opaqueTokenBytes = 32

const (
	defaultMemoryMaxEntries = 1024
	defaultMemoryTTL        = 24 * time.Hour
)

type MemoryRetention struct {
	MaxEntries int
	TTL        time.Duration
	Now        func() time.Time
}

type MemoryOwners struct {
	mu         sync.Mutex
	owners     map[app.OwnerID]time.Time
	maxEntries int
	ttl        time.Duration
	now        func() time.Time
}

var _ app.OwnerStore = (*MemoryOwners)(nil)
var _ app.CommuteStore = (*MemoryCommutes)(nil)

func NewMemoryOwners(retention ...MemoryRetention) *MemoryOwners {
	settings := normalizeMemoryRetention(retention)
	return &MemoryOwners{
		owners:     make(map[app.OwnerID]time.Time),
		maxEntries: settings.MaxEntries,
		ttl:        settings.TTL,
		now:        settings.Now,
	}
}

func (s *MemoryOwners) Create(ctx context.Context) (app.OwnerID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired(s.now())
	if len(s.owners) >= s.maxEntries {
		return "", app.ErrOwnerCapacityExceeded
	}
	for {
		token, err := newOpaqueToken()
		if err != nil {
			return "", err
		}
		id := app.OwnerID(token)
		if _, exists := s.owners[id]; !exists {
			s.owners[id] = s.now().Add(s.ttl)
			return id, nil
		}
	}
}

func (s *MemoryOwners) Touch(ctx context.Context, id app.OwnerID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictExpired(now)
	if _, exists := s.owners[id]; !exists {
		return false, nil
	}
	s.owners[id] = now.Add(s.ttl)
	return true, nil
}

func (s *MemoryOwners) Exists(ctx context.Context, id app.OwnerID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired(s.now())
	_, exists := s.owners[id]
	return exists, nil
}

func (s *MemoryOwners) Delete(ctx context.Context, id app.OwnerID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired(s.now())
	delete(s.owners, id)
	return nil
}

func (s *MemoryOwners) evictExpired(now time.Time) {
	for id, expiresAt := range s.owners {
		if !now.Before(expiresAt) {
			delete(s.owners, id)
		}
	}
}

type MemoryCommutes struct {
	mu         sync.Mutex
	commutes   map[app.OwnerID][]app.CommuteRecord
	maxEntries int
	owners     app.OwnerStore
}

func NewMemoryCommutes(owners app.OwnerStore, retention ...MemoryRetention) (*MemoryCommutes, error) {
	if owners == nil {
		return nil, errors.New("owner store is required")
	}
	settings := normalizeMemoryRetention(retention)
	return &MemoryCommutes{
		commutes:   make(map[app.OwnerID][]app.CommuteRecord),
		maxEntries: settings.MaxEntries,
		owners:     owners,
	}, nil
}

func (s *MemoryCommutes) List(ctx context.Context, ownerID app.OwnerID) ([]app.CommuteRecord, error) {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCommuteRecords(s.commutes[ownerID]), nil
}

func (s *MemoryCommutes) Create(ctx context.Context, ownerID app.OwnerID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total() >= s.maxEntries {
		return app.CommuteRecord{}, app.ErrCommuteCapacityExceeded
	}
	for {
		token, err := newOpaqueToken()
		if err != nil {
			return app.CommuteRecord{}, err
		}
		record := app.CommuteRecord{ID: app.CommuteID(token), Name: name, Commute: commute}
		if !s.hasID(ownerID, record.ID) {
			s.commutes[ownerID] = append(s.commutes[ownerID], record)
			return cloneCommuteRecord(record), nil
		}
	}
}

func (s *MemoryCommutes) Get(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) (app.CommuteRecord, error) {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.commutes[ownerID] {
		if record.ID == commuteID {
			return cloneCommuteRecord(record), nil
		}
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func (s *MemoryCommutes) Update(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, record := range s.commutes[ownerID] {
		if record.ID == commuteID {
			if commuteEndpointsChanged(record.Commute, commute) {
				record.SavedCombinations = nil
			}
			record.Name, record.Commute = name, commute
			s.commutes[ownerID][index] = record
			return cloneCommuteRecord(record), nil
		}
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func commuteEndpointsChanged(previous, next domain.Commute) bool {
	return previous.Origin().Text() != next.Origin().Text() ||
		previous.Destination().Text() != next.Destination().Text()
}

func (s *MemoryCommutes) ReplaceSavedRouteCombinations(
	ctx context.Context,
	ownerID app.OwnerID,
	commuteID app.CommuteID,
	patterns []domain.RoutePattern,
) (app.CommuteRecord, error) {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, record := range s.commutes[ownerID] {
		if record.ID != commuteID {
			continue
		}
		replacements, err := replaceSavedRouteCombinations(record.SavedCombinations, patterns)
		if err != nil {
			return app.CommuteRecord{}, err
		}
		record.SavedCombinations = replacements
		s.commutes[ownerID][index] = record
		return cloneCommuteRecord(record), nil
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func (s *MemoryCommutes) Delete(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) error {
	if err := s.requireOwner(ctx, ownerID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.commutes[ownerID]
	for index, record := range records {
		if record.ID == commuteID {
			s.commutes[ownerID] = append(records[:index], records[index+1:]...)
			if len(s.commutes[ownerID]) == 0 {
				delete(s.commutes, ownerID)
			}
			return nil
		}
	}
	return app.ErrCommuteNotFound
}

func (s *MemoryCommutes) requireOwner(ctx context.Context, ownerID app.OwnerID) error {
	exists, err := s.owners.Exists(ctx, ownerID)
	if err != nil {
		return err
	}
	if !exists {
		return app.ErrCommuteNotFound
	}
	return nil
}

func (s *MemoryCommutes) total() int {
	total := 0
	for _, records := range s.commutes {
		total += len(records)
	}
	return total
}

func (s *MemoryCommutes) hasID(ownerID app.OwnerID, commuteID app.CommuteID) bool {
	for _, record := range s.commutes[ownerID] {
		if record.ID == commuteID {
			return true
		}
	}
	return false
}

func replaceSavedRouteCombinations(existing []app.SavedRouteCombination, patterns []domain.RoutePattern) ([]app.SavedRouteCombination, error) {
	byFingerprint := make(map[string]app.SavedRouteCombination, len(existing))
	usedIDs := make(map[app.SavedCombinationID]struct{}, len(existing))
	for _, combination := range existing {
		fingerprint := combination.Pattern.Fingerprint()
		if combination.ID == "" || fingerprint == "" {
			return nil, domain.ErrInvalidRoutePattern
		}
		byFingerprint[fingerprint] = cloneSavedRouteCombination(combination)
		usedIDs[combination.ID] = struct{}{}
	}
	replacements := make([]app.SavedRouteCombination, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		fingerprint := pattern.Fingerprint()
		if fingerprint == "" {
			return nil, domain.ErrInvalidRoutePattern
		}
		if _, duplicate := seen[fingerprint]; duplicate {
			continue
		}
		seen[fingerprint] = struct{}{}
		if saved, exists := byFingerprint[fingerprint]; exists {
			saved.Pattern = pattern.Clone()
			replacements = append(replacements, saved)
			continue
		}
		for {
			token, err := newOpaqueToken()
			if err != nil {
				return nil, err
			}
			id := app.SavedCombinationID(token)
			if _, exists := usedIDs[id]; exists {
				continue
			}
			usedIDs[id] = struct{}{}
			replacements = append(replacements, app.SavedRouteCombination{ID: id, Pattern: pattern.Clone()})
			break
		}
	}
	return replacements, nil
}

func cloneCommuteRecords(records []app.CommuteRecord) []app.CommuteRecord {
	cloned := make([]app.CommuteRecord, len(records))
	for index, record := range records {
		cloned[index] = cloneCommuteRecord(record)
	}
	return cloned
}

func cloneCommuteRecord(record app.CommuteRecord) app.CommuteRecord {
	combinations := record.SavedCombinations
	record.SavedCombinations = make([]app.SavedRouteCombination, len(combinations))
	for index, combination := range combinations {
		record.SavedCombinations[index] = cloneSavedRouteCombination(combination)
	}
	return record
}

func cloneSavedRouteCombination(combination app.SavedRouteCombination) app.SavedRouteCombination {
	combination.Pattern = combination.Pattern.Clone()
	return combination
}

func normalizeMemoryRetention(retention []MemoryRetention) MemoryRetention {
	settings := MemoryRetention{MaxEntries: defaultMemoryMaxEntries, TTL: defaultMemoryTTL, Now: time.Now}
	if len(retention) == 0 {
		return settings
	}
	if retention[0].MaxEntries > 0 {
		settings.MaxEntries = retention[0].MaxEntries
	}
	if retention[0].TTL > 0 {
		settings.TTL = retention[0].TTL
	}
	if retention[0].Now != nil {
		settings.Now = retention[0].Now
	}
	return settings
}

func newOpaqueToken() (string, error) {
	bytes := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
