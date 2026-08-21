package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const (
	fileStoreVersion                 = 2
	defaultFileStoreOwnerTTL         = 90 * 24 * time.Hour
	defaultFileStoreMaxOwners        = 1024
	defaultFileStoreMaxTotalCommutes = 1024
	defaultFileStoreMaxPerOwner      = 25
	defaultFileStoreMaxBytes         = 4 << 20
)

var errFileStore = errors.New("file store error")

type FileStoreSettings struct {
	Path                string
	TTL                 time.Duration
	OwnerTTL            time.Duration
	MaxOwners           int
	MaxTotalCommutes    int
	MaxCommutesPerOwner int
	MaxBytes            int64
	Now                 func() time.Time
}

type FileStoreConfig = FileStoreSettings

type FileStoreOptions = FileStoreSettings

type FileStore struct {
	mu            sync.Mutex
	path          string
	ownerTTL      time.Duration
	maxOwners     int
	maxTotal      int
	maxPerOwner   int
	maxBytes      int64
	now           func() time.Time
	owners        map[app.OwnerID]fileOwnerState
	writeSnapshot func(fileSnapshot) (bool, error)
}

type fileOwnerStore struct{ store *FileStore }

type fileCommuteStore struct{ store *FileStore }

func (s *FileStore) Owners() app.OwnerStore { return fileOwnerStore{store: s} }

func (s *FileStore) Commutes() app.CommuteStore { return fileCommuteStore{store: s} }

func (s *FileStore) OwnerStore() app.OwnerStore { return s.Owners() }

func (s *FileStore) CommuteStore() app.CommuteStore { return s.Commutes() }

func (s fileOwnerStore) Create(ctx context.Context) (app.OwnerID, error) {
	return s.store.createOwner(ctx)
}

func (s fileOwnerStore) Touch(ctx context.Context, ownerID app.OwnerID) (bool, error) {
	return s.store.touchOwner(ctx, ownerID)
}

func (s fileOwnerStore) Exists(ctx context.Context, ownerID app.OwnerID) (bool, error) {
	return s.store.ownerExists(ctx, ownerID)
}

func (s fileOwnerStore) Delete(ctx context.Context, ownerID app.OwnerID) error {
	return s.store.deleteOwner(ctx, ownerID)
}

func (s fileCommuteStore) List(ctx context.Context, ownerID app.OwnerID) ([]app.CommuteRecord, error) {
	return s.store.listCommutes(ctx, ownerID)
}

func (s fileCommuteStore) Create(ctx context.Context, ownerID app.OwnerID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	return s.store.createCommute(ctx, ownerID, name, commute)
}

func (s fileCommuteStore) Get(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) (app.CommuteRecord, error) {
	return s.store.getCommute(ctx, ownerID, commuteID)
}

func (s fileCommuteStore) Update(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	return s.store.updateCommute(ctx, ownerID, commuteID, name, commute)
}

func (s fileCommuteStore) Delete(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) error {
	return s.store.deleteCommute(ctx, ownerID, commuteID)
}

func (s fileCommuteStore) ReplaceSavedRouteCombinations(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID, patterns []domain.RoutePattern) (app.CommuteRecord, error) {
	return s.store.replaceSavedRouteCombinations(ctx, ownerID, commuteID, patterns)
}

type fileOwnerState struct {
	expiresAt time.Time
	commutes  []app.CommuteRecord
}

type fileSnapshot struct {
	Version int         `json:"version"`
	Owners  []fileOwner `json:"owners"`
}

type fileOwner struct {
	ID        string        `json:"id"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Commutes  []fileCommute `json:"commutes"`
}

type fileCommute struct {
	ID                             string                      `json:"id"`
	Name                           string                      `json:"name"`
	Origin                         string                      `json:"origin"`
	Destination                    string                      `json:"destination"`
	ActiveDays                     []int                       `json:"activeDays"`
	DepartureTime                  string                      `json:"departureTime"`
	AccessToFirstBoard             int64                       `json:"accessToFirstBoard"`
	Interchange                    int64                       `json:"interchange"`
	MaximumInitialWalk             int64                       `json:"maximumInitialWalk,omitempty"`
	SaintLazareMinimumTransferTime int64                       `json:"saintLazareMinimumTransferTime,omitempty"`
	PreferredLines                 string                      `json:"preferredLines,omitempty"`
	SavedCombinations              []fileSavedRouteCombination `json:"savedCombinations,omitempty"`
}

type fileSavedRouteCombination struct {
	ID   string                `json:"id"`
	Legs []fileRoutePatternLeg `json:"legs"`
}

type fileRoutePatternLeg struct {
	Line       string `json:"line"`
	BoardAt    string `json:"boardAt"`
	AlightAt   string `json:"alightAt"`
	BoardName  string `json:"boardName,omitempty"`
	AlightName string `json:"alightName,omitempty"`
	Network    string `json:"network"`
	Code       string `json:"code"`
}

var _ app.OwnerStore = fileOwnerStore{}
var _ app.CommuteStore = fileCommuteStore{}

func NewFileStore(config ...FileStoreSettings) (*FileStore, error) {
	settings, err := normalizeFileStoreSettings(config)
	if err != nil {
		return nil, errFileStore
	}
	store := &FileStore{
		path:        settings.Path,
		ownerTTL:    settings.OwnerTTL,
		maxOwners:   settings.MaxOwners,
		maxTotal:    settings.MaxTotalCommutes,
		maxPerOwner: settings.MaxCommutesPerOwner,
		maxBytes:    settings.MaxBytes,
		now:         settings.Now,
		owners:      make(map[app.OwnerID]fileOwnerState),
	}
	store.writeSnapshot = store.persistSnapshot
	missing, err := ensureFileStoreParent(store.path)
	if err != nil {
		return nil, errFileStore
	}
	if missing {
		if _, err := store.writeSnapshot(store.snapshot(store.owners)); err != nil {
			return nil, errFileStore
		}
		return store, nil
	}
	if err := repairSnapshotMode(store.path); err != nil {
		return nil, errFileStore
	}
	owners, err := store.load()
	if err != nil {
		return nil, errFileStore
	}
	store.owners = owners
	return store, nil
}

func (s *FileStore) createOwner(ctx context.Context) (app.OwnerID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	if len(owners) >= s.maxOwners {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return "", err
		}
		return "", app.ErrOwnerCapacityExceeded
	}
	for {
		token, err := newOpaqueToken()
		if err != nil {
			return "", errFileStore
		}
		id := app.OwnerID(token)
		if _, exists := owners[id]; !exists {
			owners[id] = fileOwnerState{expiresAt: s.now().Add(s.ownerTTL)}
			if err := s.commit(owners); err != nil {
				return "", err
			}
			return id, nil
		}
	}
}

func (s *FileStore) touchOwner(ctx context.Context, ownerID app.OwnerID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if !exists {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return false, err
		}
		return false, nil
	}
	owner.expiresAt = s.now().Add(s.ownerTTL)
	owners[ownerID] = owner
	if err := s.commit(owners); err != nil {
		return false, err
	}
	return true, nil
}

func (s *FileStore) ownerExists(ctx context.Context, ownerID app.OwnerID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	if err := s.commitIfChanged(owners, changed); err != nil {
		return false, err
	}
	_, exists := owners[ownerID]
	return exists, nil
}

func (s *FileStore) deleteOwner(ctx context.Context, ownerID app.OwnerID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	if _, exists := owners[ownerID]; exists {
		delete(owners, ownerID)
		changed = true
	}
	return s.commitIfChanged(owners, changed)
}

func (s *FileStore) listCommutes(ctx context.Context, ownerID app.OwnerID) ([]app.CommuteRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if err := s.commitIfChanged(owners, changed); err != nil {
		return nil, err
	}
	if !exists {
		return nil, app.ErrCommuteNotFound
	}
	return cloneCommuteRecords(owner.commutes), nil
}

func (s *FileStore) createCommute(ctx context.Context, ownerID app.OwnerID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	if err := ctx.Err(); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if !exists {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return app.CommuteRecord{}, err
		}
		return app.CommuteRecord{}, app.ErrCommuteNotFound
	}
	if len(owner.commutes) >= s.maxPerOwner || countCommutes(owners) >= s.maxTotal {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return app.CommuteRecord{}, err
		}
		return app.CommuteRecord{}, app.ErrCommuteCapacityExceeded
	}
	if !validCommute(name, commute) {
		return app.CommuteRecord{}, errFileStore
	}
	for {
		token, err := newOpaqueToken()
		if err != nil {
			return app.CommuteRecord{}, errFileStore
		}
		record := app.CommuteRecord{ID: app.CommuteID(token), Name: name, Commute: commute}
		if !containsCommute(owner.commutes, record.ID) {
			owner.commutes = append(owner.commutes, record)
			owners[ownerID] = owner
			if err := s.commit(owners); err != nil {
				return app.CommuteRecord{}, err
			}
			return record, nil
		}
	}
}

func (s *FileStore) getCommute(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) (app.CommuteRecord, error) {
	if err := ctx.Err(); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if err := s.commitIfChanged(owners, changed); err != nil {
		return app.CommuteRecord{}, err
	}
	if !exists {
		return app.CommuteRecord{}, app.ErrCommuteNotFound
	}
	for _, record := range owner.commutes {
		if record.ID == commuteID {
			return cloneCommuteRecord(record), nil
		}
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func (s *FileStore) updateCommute(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID, name domain.CommuteName, commute domain.Commute) (app.CommuteRecord, error) {
	if err := ctx.Err(); err != nil {
		return app.CommuteRecord{}, err
	}
	if !validCommute(name, commute) {
		return app.CommuteRecord{}, errFileStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if !exists {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return app.CommuteRecord{}, err
		}
		return app.CommuteRecord{}, app.ErrCommuteNotFound
	}
	for index, record := range owner.commutes {
		if record.ID == commuteID {
			if commuteEndpointsChanged(record.Commute, commute) {
				record.SavedCombinations = nil
			}
			record.Name, record.Commute = name, commute
			owner.commutes[index] = record
			owners[ownerID] = owner
			if err := s.commit(owners); err != nil {
				return app.CommuteRecord{}, err
			}
			return record, nil
		}
	}
	if err := s.commitIfChanged(owners, changed); err != nil {
		return app.CommuteRecord{}, err
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func (s *FileStore) deleteCommute(ctx context.Context, ownerID app.OwnerID, commuteID app.CommuteID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if !exists {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return err
		}
		return app.ErrCommuteNotFound
	}
	for index, record := range owner.commutes {
		if record.ID == commuteID {
			owner.commutes = append(owner.commutes[:index], owner.commutes[index+1:]...)
			owners[ownerID] = owner
			return s.commit(owners)
		}
	}
	if err := s.commitIfChanged(owners, changed); err != nil {
		return err
	}
	return app.ErrCommuteNotFound
}

func (s *FileStore) replaceSavedRouteCombinations(
	ctx context.Context,
	ownerID app.OwnerID,
	commuteID app.CommuteID,
	patterns []domain.RoutePattern,
) (app.CommuteRecord, error) {
	if err := ctx.Err(); err != nil {
		return app.CommuteRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owners, changed := s.cloneAndExpire()
	owner, exists := owners[ownerID]
	if !exists {
		if err := s.commitIfChanged(owners, changed); err != nil {
			return app.CommuteRecord{}, err
		}
		return app.CommuteRecord{}, app.ErrCommuteNotFound
	}
	for index, record := range owner.commutes {
		if record.ID != commuteID {
			continue
		}
		replacements, err := replaceSavedRouteCombinations(record.SavedCombinations, patterns)
		if err != nil {
			return app.CommuteRecord{}, err
		}
		record.SavedCombinations = replacements
		owner.commutes[index] = record
		owners[ownerID] = owner
		if err := s.commit(owners); err != nil {
			return app.CommuteRecord{}, err
		}
		return cloneCommuteRecord(record), nil
	}
	if err := s.commitIfChanged(owners, changed); err != nil {
		return app.CommuteRecord{}, err
	}
	return app.CommuteRecord{}, app.ErrCommuteNotFound
}

func normalizeFileStoreSettings(config []FileStoreSettings) (FileStoreSettings, error) {
	settings := FileStoreSettings{
		OwnerTTL:            defaultFileStoreOwnerTTL,
		MaxOwners:           defaultFileStoreMaxOwners,
		MaxTotalCommutes:    defaultFileStoreMaxTotalCommutes,
		MaxCommutesPerOwner: defaultFileStoreMaxPerOwner,
		MaxBytes:            defaultFileStoreMaxBytes,
		Now:                 time.Now,
	}
	if len(config) > 1 {
		return FileStoreSettings{}, errors.New("invalid file store settings")
	}
	if len(config) == 1 {
		provided := config[0]
		settings.Path = provided.Path
		if provided.TTL > 0 {
			settings.OwnerTTL = provided.TTL
		}
		if provided.OwnerTTL > 0 {
			settings.OwnerTTL = provided.OwnerTTL
		}
		if provided.MaxOwners > 0 {
			settings.MaxOwners = provided.MaxOwners
		}
		if provided.MaxTotalCommutes > 0 {
			settings.MaxTotalCommutes = provided.MaxTotalCommutes
		}
		if provided.MaxCommutesPerOwner > 0 {
			settings.MaxCommutesPerOwner = provided.MaxCommutesPerOwner
		}
		if provided.MaxBytes > 0 {
			settings.MaxBytes = provided.MaxBytes
		}
		if provided.Now != nil {
			settings.Now = provided.Now
		}
	}
	if settings.Path == "" || settings.OwnerTTL <= 0 || settings.MaxOwners <= 0 || settings.MaxTotalCommutes <= 0 || settings.MaxCommutesPerOwner <= 0 || settings.MaxBytes <= 0 {
		return FileStoreSettings{}, errors.New("invalid file store settings")
	}
	return settings, nil
}

func ensureFileStoreParent(path string) (bool, error) {
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	createdDirectory := os.IsNotExist(statErr)
	if statErr != nil && !createdDirectory {
		return false, statErr
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return false, err
	}
	if createdDirectory {
		if err := os.Chmod(directory, 0o700); err != nil {
			return false, err
		}
	}
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func repairSnapshotMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	return os.Chmod(path, 0o600)
}

func (s *FileStore) load() (owners map[app.OwnerID]fileOwnerState, err error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
			owners = nil
		}
	}()
	data, err := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	if err != nil || int64(len(data)) > s.maxBytes || len(data) == 0 {
		return nil, errors.New("invalid snapshot")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot fileSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid snapshot")
	}
	return s.ownersFromSnapshot(snapshot)
}

func (s *FileStore) ownersFromSnapshot(snapshot fileSnapshot) (map[app.OwnerID]fileOwnerState, error) {
	if (snapshot.Version != 1 && snapshot.Version != fileStoreVersion) || snapshot.Owners == nil || len(snapshot.Owners) > s.maxOwners {
		return nil, errors.New("invalid snapshot")
	}
	owners := make(map[app.OwnerID]fileOwnerState, len(snapshot.Owners))
	total := 0
	for _, savedOwner := range snapshot.Owners {
		id := app.OwnerID(savedOwner.ID)
		if !validOpaqueID(savedOwner.ID) || savedOwner.ExpiresAt.IsZero() || len(savedOwner.Commutes) > s.maxPerOwner {
			return nil, errors.New("invalid snapshot")
		}
		if _, exists := owners[id]; exists {
			return nil, errors.New("invalid snapshot")
		}
		owner := fileOwnerState{expiresAt: savedOwner.ExpiresAt, commutes: make([]app.CommuteRecord, 0, len(savedOwner.Commutes))}
		for _, savedCommute := range savedOwner.Commutes {
			if !validOpaqueID(savedCommute.ID) || containsCommute(owner.commutes, app.CommuteID(savedCommute.ID)) {
				return nil, errors.New("invalid snapshot")
			}
			name, err := domain.NewCommuteName(savedCommute.Name)
			if err != nil {
				return nil, err
			}
			commute, err := commuteFromFile(savedCommute)
			if err != nil {
				return nil, err
			}
			savedCombinations, err := savedCombinationsFromFile(savedCommute.SavedCombinations)
			if err != nil {
				return nil, err
			}
			owner.commutes = append(owner.commutes, app.CommuteRecord{ID: app.CommuteID(savedCommute.ID), Name: name, Commute: commute, SavedCombinations: savedCombinations})
			total++
			if total > s.maxTotal {
				return nil, errors.New("invalid snapshot")
			}
		}
		owners[id] = owner
	}
	return owners, nil
}

func (s *FileStore) cloneAndExpire() (map[app.OwnerID]fileOwnerState, bool) {
	owners := cloneOwners(s.owners)
	changed := false
	now := s.now()
	for id, owner := range owners {
		if !now.Before(owner.expiresAt) {
			delete(owners, id)
			changed = true
		}
	}
	return owners, changed
}

func (s *FileStore) commitIfChanged(owners map[app.OwnerID]fileOwnerState, changed bool) error {
	if !changed {
		return nil
	}
	return s.commit(owners)
}

func (s *FileStore) commit(owners map[app.OwnerID]fileOwnerState) error {
	if err := s.validateOwners(owners); err != nil {
		return errFileStore
	}
	canonical, err := s.writeSnapshot(s.snapshot(owners))
	if canonical {
		s.owners = owners
	}
	if err != nil {
		return errFileStore
	}
	return nil
}

func (s *FileStore) validateOwners(owners map[app.OwnerID]fileOwnerState) error {
	if len(owners) > s.maxOwners || countCommutes(owners) > s.maxTotal {
		return errors.New("invalid owners")
	}
	for id, owner := range owners {
		if !validOpaqueID(string(id)) || owner.expiresAt.IsZero() || len(owner.commutes) > s.maxPerOwner {
			return errors.New("invalid owner")
		}
		seen := make(map[app.CommuteID]struct{}, len(owner.commutes))
		for _, record := range owner.commutes {
			if !validOpaqueID(string(record.ID)) || !validCommute(record.Name, record.Commute) || !validSavedRouteCombinations(record.SavedCombinations) {
				return errors.New("invalid commute")
			}
			if _, exists := seen[record.ID]; exists {
				return errors.New("invalid commute")
			}
			seen[record.ID] = struct{}{}
		}
	}
	return nil
}

func (s *FileStore) snapshot(owners map[app.OwnerID]fileOwnerState) fileSnapshot {
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	snapshot := fileSnapshot{Version: fileStoreVersion, Owners: make([]fileOwner, 0, len(ids))}
	for _, rawID := range ids {
		owner := owners[app.OwnerID(rawID)]
		saved := fileOwner{ID: rawID, ExpiresAt: owner.expiresAt, Commutes: make([]fileCommute, 0, len(owner.commutes))}
		for _, record := range owner.commutes {
			saved.Commutes = append(saved.Commutes, commuteToFile(record))
		}
		snapshot.Owners = append(snapshot.Owners, saved)
	}
	return snapshot
}

func (s *FileStore) persistSnapshot(snapshot fileSnapshot) (canonical bool, err error) {
	data, err := json.Marshal(snapshot)
	if err != nil || int64(len(data)) > s.maxBytes {
		return false, errors.New("snapshot too large")
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".owners-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); err == nil && !os.IsNotExist(removeErr) && removeErr != nil {
			err = removeErr
		}
	}()
	temporaryClosed := false
	closeTemporary := func() error {
		if temporaryClosed {
			return nil
		}
		temporaryClosed = true
		return temporary.Close()
	}
	defer func() {
		if closeErr := closeTemporary(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return false, err
	}
	if _, err := temporary.Write(data); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := closeTemporary(); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return false, err
	}
	canonical = true
	dir, err := os.Open(directory)
	if err != nil {
		return canonical, err
	}
	defer func() {
		if closeErr := dir.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return canonical, dir.Sync()
}

func commuteToFile(record app.CommuteRecord) fileCommute {
	commute := record.Commute
	schedule := commute.Schedule()
	days := make([]int, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		if schedule.IsActive(day) {
			days = append(days, int(day))
		}
	}
	savedCombinations := make([]fileSavedRouteCombination, 0, len(record.SavedCombinations))
	for _, combination := range record.SavedCombinations {
		savedCombinations = append(savedCombinations, savedRouteCombinationToFile(combination))
	}
	return fileCommute{
		ID:                 string(record.ID),
		Name:               record.Name.Text(),
		Origin:             commute.Origin().Text(),
		Destination:        commute.Destination().Text(),
		ActiveDays:         days,
		DepartureTime:      schedule.DepartureTime().String(),
		AccessToFirstBoard: int64(commute.MaximumInitialWalk().Duration()),
		Interchange:        int64(commute.SaintLazareMinimumTransferTime().Duration()),
		PreferredLines:     commute.PreferredLines().Raw(),
		SavedCombinations:  savedCombinations,
	}
}

func savedRouteCombinationToFile(combination app.SavedRouteCombination) fileSavedRouteCombination {
	legs := combination.Pattern.Legs()
	saved := fileSavedRouteCombination{ID: string(combination.ID), Legs: make([]fileRoutePatternLeg, 0, len(legs))}
	for _, leg := range legs {
		label := leg.Label()
		saved.Legs = append(saved.Legs, fileRoutePatternLeg{
			Line:       string(leg.Line()),
			BoardAt:    string(leg.BoardAt()),
			AlightAt:   string(leg.AlightAt()),
			BoardName:  leg.BoardName(),
			AlightName: leg.AlightName(),
			Network:    label.Network(),
			Code:       label.Code(),
		})
	}
	return saved
}

func savedCombinationsFromFile(saved []fileSavedRouteCombination) ([]app.SavedRouteCombination, error) {
	combinations := make([]app.SavedRouteCombination, 0, len(saved))
	seenIDs := make(map[app.SavedCombinationID]struct{}, len(saved))
	seenPatterns := make(map[string]struct{}, len(saved))
	for _, raw := range saved {
		id := app.SavedCombinationID(raw.ID)
		if !validOpaqueID(raw.ID) {
			return nil, errors.New("invalid saved route combination")
		}
		if _, exists := seenIDs[id]; exists {
			return nil, errors.New("invalid saved route combination")
		}
		legs := make([]domain.PatternLeg, 0, len(raw.Legs))
		for _, rawLeg := range raw.Legs {
			leg, err := domain.NewPatternLegWithNames(
				domain.ProviderLineID(rawLeg.Line),
				domain.ProviderStopID(rawLeg.BoardAt),
				domain.ProviderStopID(rawLeg.AlightAt),
				rawLeg.BoardName,
				rawLeg.AlightName,
				domain.NewTransitLabel(rawLeg.Network, rawLeg.Code),
			)
			if err != nil {
				return nil, err
			}
			legs = append(legs, leg)
		}
		pattern, err := domain.NewRoutePattern(legs)
		if err != nil {
			return nil, err
		}
		fingerprint := pattern.Fingerprint()
		if _, exists := seenPatterns[fingerprint]; exists {
			return nil, errors.New("invalid saved route combination")
		}
		seenIDs[id] = struct{}{}
		seenPatterns[fingerprint] = struct{}{}
		combinations = append(combinations, app.SavedRouteCombination{ID: id, Pattern: pattern})
	}
	return combinations, nil
}

func commuteFromFile(saved fileCommute) (domain.Commute, error) {
	origin, err := domain.NewPlace(saved.Origin)
	if err != nil {
		return domain.Commute{}, err
	}
	destination, err := domain.NewPlace(saved.Destination)
	if err != nil {
		return domain.Commute{}, err
	}
	departure, err := domain.NewDepartureTime(saved.DepartureTime)
	if err != nil {
		return domain.Commute{}, err
	}
	days := make([]time.Weekday, 0, len(saved.ActiveDays))
	seen := make(map[int]struct{}, len(saved.ActiveDays))
	for _, day := range saved.ActiveDays {
		if day < int(time.Sunday) || day > int(time.Saturday) {
			return domain.Commute{}, errors.New("invalid day")
		}
		if _, exists := seen[day]; exists {
			return domain.Commute{}, errors.New("invalid day")
		}
		seen[day] = struct{}{}
		days = append(days, time.Weekday(day))
	}
	schedule, err := domain.NewWeeklySchedule(days, departure)
	if err != nil {
		return domain.Commute{}, err
	}
	maximumInitialWalk, err := domain.NewTransferBuffer(time.Duration(saved.accessDuration()))
	if err != nil {
		return domain.Commute{}, err
	}
	minimumTransfer, err := domain.NewTransferBuffer(time.Duration(saved.interchangeDuration()))
	if err != nil {
		return domain.Commute{}, err
	}
	preferredLines := domain.NewPreferredLines(saved.PreferredLines)
	return domain.NewCommute(origin, destination, schedule, maximumInitialWalk, minimumTransfer, preferredLines)
}

func validCommute(name domain.CommuteName, commute domain.Commute) bool {
	_, err := domain.NewCommuteName(name.Text())
	if err != nil {
		return false
	}
	_, err = commuteFromFile(commuteToFile(app.CommuteRecord{Name: name, Commute: commute}))
	return err == nil
}

func cloneOwners(source map[app.OwnerID]fileOwnerState) map[app.OwnerID]fileOwnerState {
	cloned := make(map[app.OwnerID]fileOwnerState, len(source))
	for id, owner := range source {
		owner.commutes = cloneCommuteRecords(owner.commutes)
		cloned[id] = owner
	}
	return cloned
}

func validSavedRouteCombinations(combinations []app.SavedRouteCombination) bool {
	seenIDs := make(map[app.SavedCombinationID]struct{}, len(combinations))
	seenPatterns := make(map[string]struct{}, len(combinations))
	for _, combination := range combinations {
		fingerprint := combination.Pattern.Fingerprint()
		if !validOpaqueID(string(combination.ID)) || fingerprint == "" {
			return false
		}
		if _, exists := seenIDs[combination.ID]; exists {
			return false
		}
		if _, exists := seenPatterns[fingerprint]; exists {
			return false
		}
		seenIDs[combination.ID] = struct{}{}
		seenPatterns[fingerprint] = struct{}{}
	}
	return true
}

func countCommutes(owners map[app.OwnerID]fileOwnerState) int {
	total := 0
	for _, owner := range owners {
		total += len(owner.commutes)
	}
	return total
}

func containsCommute(records []app.CommuteRecord, id app.CommuteID) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func validOpaqueID(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == opaqueTokenBytes && base64.RawURLEncoding.EncodeToString(decoded) == value
}

// Files written before the rename carry the old keys; their values are the same
// durations, so they are read as-is rather than migrated in place.
func (c fileCommute) accessDuration() int64 {
	if c.AccessToFirstBoard != 0 {
		return c.AccessToFirstBoard
	}
	return c.MaximumInitialWalk
}

func (c fileCommute) interchangeDuration() int64 {
	if c.Interchange != 0 {
		return c.Interchange
	}
	return c.SaintLazareMinimumTransferTime
}
