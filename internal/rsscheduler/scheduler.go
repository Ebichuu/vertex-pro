package rsscheduler

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type JitterFunc func(base time.Duration, ratio float64) time.Duration

type Config struct {
	BaseInterval time.Duration
	MaxInterval  time.Duration
	JitterRatio  float64
	Clock        Clock
	Jitter       JitterFunc
}

type Job struct {
	ID                  string
	Enabled             bool
	NextRunAt           time.Time
	ConsecutiveFailures int
}

type Store interface {
	Load(context.Context) ([]Job, error)
	Save(context.Context, []Job) error
}

type RunResult struct {
	JobID     string
	Started   time.Time
	NextRunAt time.Time
	Err       error
}

type Scheduler struct {
	config  Config
	store   Store
	mu      sync.Mutex
	jobs    map[string]Job
	running map[string]bool
}

func New(config Config, store Store) (*Scheduler, error) {
	if config.BaseInterval <= 0 || config.MaxInterval < config.BaseInterval {
		return nil, errors.New("invalid scheduler intervals")
	}
	if config.JitterRatio < 0 || config.JitterRatio > 1 {
		return nil, errors.New("jitter ratio must be between 0 and 1")
	}
	if config.Clock == nil {
		config.Clock = RealClock{}
	}
	if config.Jitter == nil {
		config.Jitter = randomJitter
	}
	if store == nil {
		return nil, errors.New("scheduler store is required")
	}
	return &Scheduler{config: config, store: store, jobs: make(map[string]Job), running: make(map[string]bool)}, nil
}

func (scheduler *Scheduler) Restore(ctx context.Context) error {
	jobs, err := scheduler.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load scheduler jobs: %w", err)
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.jobs = make(map[string]Job, len(jobs))
	scheduler.running = make(map[string]bool)
	for _, job := range jobs {
		if job.ID == "" || job.NextRunAt.IsZero() {
			return errors.New("invalid persisted scheduler job")
		}
		job.NextRunAt = job.NextRunAt.UTC()
		scheduler.jobs[job.ID] = job
	}
	return nil
}

func (scheduler *Scheduler) Register(ctx context.Context, id string, enabled bool) error {
	if id == "" {
		return errors.New("job ID is required")
	}
	now := scheduler.config.Clock.Now().UTC()
	scheduler.mu.Lock()
	if _, exists := scheduler.jobs[id]; !exists {
		scheduler.jobs[id] = Job{ID: id, Enabled: enabled, NextRunAt: now}
	} else {
		job := scheduler.jobs[id]
		job.Enabled = enabled
		scheduler.jobs[id] = job
	}
	scheduler.mu.Unlock()
	return scheduler.persist(ctx)
}

func (scheduler *Scheduler) Disable(ctx context.Context, id string) error {
	scheduler.mu.Lock()
	job, exists := scheduler.jobs[id]
	if !exists {
		scheduler.mu.Unlock()
		return fmt.Errorf("job %q not found", id)
	}
	job.Enabled = false
	scheduler.jobs[id] = job
	scheduler.mu.Unlock()
	return scheduler.persist(ctx)
}

func (scheduler *Scheduler) Jobs() []Job {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	result := make([]Job, 0, len(scheduler.jobs))
	for _, job := range scheduler.jobs {
		result = append(result, job)
	}
	return result
}

func (scheduler *Scheduler) Tick(ctx context.Context, run func(context.Context, Job) error) ([]RunResult, error) {
	if run == nil {
		return nil, errors.New("run callback is required")
	}
	now := scheduler.config.Clock.Now().UTC()
	jobs := scheduler.dueJobs(now)
	results := make([]RunResult, 0, len(jobs))
	for _, job := range jobs {
		started := scheduler.config.Clock.Now().UTC()
		runErr := run(ctx, job)
		next, err := scheduler.complete(ctx, job.ID, runErr)
		if err != nil {
			return results, err
		}
		results = append(results, RunResult{JobID: job.ID, Started: started, NextRunAt: next, Err: runErr})
	}
	return results, nil
}

func (scheduler *Scheduler) dueJobs(now time.Time) []Job {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	result := make([]Job, 0)
	for _, job := range scheduler.jobs {
		if job.Enabled && !job.NextRunAt.After(now) && !scheduler.running[job.ID] {
			scheduler.running[job.ID] = true
			result = append(result, job)
		}
	}
	return result
}

func (scheduler *Scheduler) complete(ctx context.Context, id string, runErr error) (time.Time, error) {
	scheduler.mu.Lock()
	job, exists := scheduler.jobs[id]
	if !exists {
		scheduler.running[id] = false
		scheduler.mu.Unlock()
		return time.Time{}, fmt.Errorf("job %q not found", id)
	}
	if runErr == nil {
		job.ConsecutiveFailures = 0
	} else {
		job.ConsecutiveFailures++
	}
	interval := scheduler.config.BaseInterval
	for attempt := 1; attempt < job.ConsecutiveFailures; attempt++ {
		if interval >= scheduler.config.MaxInterval/2 {
			interval = scheduler.config.MaxInterval
			break
		}
		interval *= 2
	}
	if interval > scheduler.config.MaxInterval {
		interval = scheduler.config.MaxInterval
	}
	interval = scheduler.config.Jitter(interval, scheduler.config.JitterRatio)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	job.NextRunAt = scheduler.config.Clock.Now().UTC().Add(interval)
	scheduler.jobs[id] = job
	next := job.NextRunAt
	scheduler.running[id] = false
	scheduler.mu.Unlock()
	if err := scheduler.persist(ctx); err != nil {
		return time.Time{}, err
	}
	return next, nil
}
func (scheduler *Scheduler) persist(ctx context.Context) error {
	return scheduler.store.Save(ctx, scheduler.Jobs())
}

func randomJitter(base time.Duration, ratio float64) time.Duration {
	if ratio == 0 {
		return base
	}
	randomMu.Lock()
	value := rand.Float64()
	randomMu.Unlock()
	factor := 1 - ratio + value*2*ratio
	return time.Duration(float64(base) * factor)
}

var randomMu sync.Mutex

type MemoryStore struct {
	mu   sync.Mutex
	jobs []Job
}

func (store *MemoryStore) Load(context.Context) ([]Job, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneJobs(store.jobs), nil
}

func (store *MemoryStore) Save(_ context.Context, jobs []Job) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.jobs = cloneJobs(jobs)
	return nil
}

func cloneJobs(jobs []Job) []Job {
	result := make([]Job, len(jobs))
	copy(result, jobs)
	return result
}
