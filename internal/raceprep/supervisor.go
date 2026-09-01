package raceprep

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Kind string

const (
	RuleKind      Kind = "rule"
	TorrentKind   Kind = "torrent"
	PlacementKind Kind = "placement"
)

type Worker func(context.Context) error

type Workers struct {
	Rule      Worker
	Torrent   Worker
	Placement Worker
}
type Outcome struct {
	Kind     Kind
	Err      error
	Panicked bool
}
type Result struct {
	Outcomes map[Kind]Outcome
	Ready    bool
}

type Supervisor struct {
	mu      sync.Mutex
	running bool
}

func (supervisor *Supervisor) Run(ctx context.Context, workers Workers) (Result, error) {
	if supervisor == nil {
		return Result{}, errors.New("supervisor is nil")
	}
	if ctx == nil {
		return Result{}, errors.New("context is nil")
	}
	if workers.Rule == nil || workers.Torrent == nil || workers.Placement == nil {
		return Result{}, errors.New("all preparation workers are required")
	}
	supervisor.mu.Lock()
	if supervisor.running {
		supervisor.mu.Unlock()
		return Result{}, errors.New("preparation run is already in progress")
	}
	supervisor.running = true
	supervisor.mu.Unlock()
	defer func() { supervisor.mu.Lock(); supervisor.running = false; supervisor.mu.Unlock() }()

	jobs := []struct {
		kind   Kind
		worker Worker
	}{{RuleKind, workers.Rule}, {TorrentKind, workers.Torrent}, {PlacementKind, workers.Placement}}
	results := make(chan Outcome, len(jobs))
	var wait sync.WaitGroup
	for _, job := range jobs {
		wait.Add(1)
		go func(kind Kind, worker Worker) { defer wait.Done(); results <- execute(ctx, kind, worker) }(job.kind, job.worker)
	}
	wait.Wait()
	close(results)
	result := Result{Outcomes: make(map[Kind]Outcome, len(jobs)), Ready: true}
	for outcome := range results {
		result.Outcomes[outcome.Kind] = outcome
		if outcome.Err != nil {
			result.Ready = false
		}
	}
	return result, nil
}

func execute(ctx context.Context, kind Kind, worker Worker) (outcome Outcome) {
	outcome.Kind = kind
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome.Panicked = true
			outcome.Err = fmt.Errorf("%s worker panicked: %v", kind, recovered)
		}
	}()
	if err := ctx.Err(); err != nil {
		outcome.Err = fmt.Errorf("%s worker cancelled: %w", kind, err)
		return outcome
	}
	if err := worker(ctx); err != nil {
		outcome.Err = fmt.Errorf("%s worker: %w", kind, err)
	}
	return outcome
}
