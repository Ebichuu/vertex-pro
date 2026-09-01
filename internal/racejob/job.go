package racejob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Clock interface{ Now() time.Time }
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Input struct {
	SourceID              string
	IdentityKey           string
	ParticipationDeadline time.Time
	ObservationDeadline   time.Time
}

type Job struct {
	ID                    string
	SourceID              string
	IdentityKey           string
	JobState              string
	RuleState             string
	TorrentState          string
	PlacementState        string
	ObservationState      string
	CurrentAttemptID      *string
	PreparationRevision   int
	ParticipationDeadline time.Time
	ObservationDeadline   time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Result struct {
	Job       Job
	Created   bool
	Duplicate bool
}

type Store interface {
	LoadBySource(context.Context, string) (Job, bool, error)
	Save(context.Context, Job) error
}

type Creator struct {
	store Store
	clock Clock
	mu    sync.Mutex
}

func New(store Store, clock Clock) (*Creator, error) {
	if store == nil {
		return nil, errors.New("race job store is required")
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Creator{store: store, clock: clock}, nil
}

func (creator *Creator) Create(ctx context.Context, input Input) (Result, error) {
	if creator == nil || creator.store == nil {
		return Result{}, errors.New("race job creator is not initialized")
	}
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.IdentityKey = strings.TrimSpace(input.IdentityKey)
	if input.SourceID == "" {
		return Result{}, errors.New("source ID is required")
	}
	if input.ParticipationDeadline.IsZero() || input.ObservationDeadline.IsZero() {
		return Result{}, errors.New("deadlines are required")
	}
	participation := input.ParticipationDeadline.UTC()
	observation := input.ObservationDeadline.UTC()
	if observation.Before(participation) {
		return Result{}, errors.New("observation deadline must not precede participation deadline")
	}
	creator.mu.Lock()
	defer creator.mu.Unlock()
	existing, found, err := creator.store.LoadBySource(ctx, input.SourceID)
	if err != nil {
		return Result{}, fmt.Errorf("load race job: %w", err)
	}
	if found {
		if existing.IdentityKey != input.IdentityKey {
			return Result{}, errors.New("source already belongs to another identity")
		}
		return Result{Job: existing, Duplicate: true}, nil
	}
	now := creator.clock.Now().UTC()
	job := Job{ID: deterministicID(input.SourceID), SourceID: input.SourceID, IdentityKey: input.IdentityKey, JobState: "DISCOVERED", RuleState: "PENDING", TorrentState: "PENDING", PlacementState: "PENDING", ObservationState: "NOT_STARTED", ParticipationDeadline: participation, ObservationDeadline: observation, CreatedAt: now, UpdatedAt: now}
	if err := creator.store.Save(ctx, job); err != nil {
		return Result{}, fmt.Errorf("save race job: %w", err)
	}
	return Result{Job: job, Created: true}, nil
}

func deterministicID(sourceID string) string {
	digest := sha256.Sum256([]byte(sourceID))
	return "job-" + hex.EncodeToString(digest[:])
}

type MemoryStore struct {
	mu   sync.Mutex
	jobs map[string]Job
}

func (store *MemoryStore) LoadBySource(_ context.Context, sourceID string) (Job, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[sourceID]
	return job, ok, nil
}
func (store *MemoryStore) Save(_ context.Context, job Job) error {
	if job.ID == "" || job.SourceID == "" {
		return errors.New("job ID and source ID are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs == nil {
		store.jobs = make(map[string]Job)
	}
	if existing, ok := store.jobs[job.SourceID]; ok && existing.ID != job.ID {
		return errors.New("source already has a different job")
	}
	store.jobs[job.SourceID] = job
	return nil
}
