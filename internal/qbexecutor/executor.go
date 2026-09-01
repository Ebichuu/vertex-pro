package qbexecutor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
)

var (
	ErrInstanceNotRegistered = errors.New("qB instance executor is not registered")
	ErrCircuitOpen           = errors.New("qB instance circuit is open")
)

type Config struct {
	MaxInFlight      int
	Timeout          time.Duration
	FailureThreshold int
	Cooldown         time.Duration
}

type Snapshot struct {
	InstanceID          string
	Open                bool
	OpenUntil           time.Time
	InFlight            int
	ConsecutiveFailures int
	TotalDispatches     uint64
	FailedDispatches    uint64
	LastLatency         time.Duration
}

type Set struct {
	mu        sync.RWMutex
	executors map[string]*executor
}

type executor struct {
	instanceID       string
	worker           *dispatchworker.Worker
	slots            chan struct{}
	timeout          time.Duration
	failureThreshold int
	cooldown         time.Duration

	mu                  sync.Mutex
	openUntil           time.Time
	inFlight            int
	consecutiveFailures int
	totalDispatches     uint64
	failedDispatches    uint64
	lastLatency         time.Duration
}

func New() *Set {
	return &Set{executors: make(map[string]*executor)}
}

func (set *Set) Register(instanceID string, sender dispatchworker.Sender, config Config) error {
	if set == nil {
		return errors.New("executor set is nil")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return errors.New("instance ID is required")
	}
	if sender == nil {
		return errors.New("dispatch sender is required")
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return err
	}
	worker, err := dispatchworker.New(sender)
	if err != nil {
		return err
	}
	candidate := &executor{
		instanceID:       instanceID,
		worker:           worker,
		slots:            make(chan struct{}, config.MaxInFlight),
		timeout:          config.Timeout,
		failureThreshold: config.FailureThreshold,
		cooldown:         config.Cooldown,
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	if _, exists := set.executors[instanceID]; exists {
		return errors.New("qB instance executor is already registered")
	}
	set.executors[instanceID] = candidate
	return nil
}

func (set *Set) Dispatch(ctx context.Context, instanceID string, input dispatchworker.Request) (dispatchworker.Result, error) {
	if set == nil {
		return dispatchworker.Result{}, errors.New("executor set is nil")
	}
	if ctx == nil {
		return dispatchworker.Result{}, errors.New("context is nil")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return dispatchworker.Result{}, errors.New("instance ID is required")
	}
	set.mu.RLock()
	instance := set.executors[instanceID]
	set.mu.RUnlock()
	if instance == nil {
		return dispatchworker.Result{}, ErrInstanceNotRegistered
	}
	return instance.dispatch(ctx, input)
}

func (set *Set) Snapshot(instanceID string) (Snapshot, error) {
	if set == nil {
		return Snapshot{}, errors.New("executor set is nil")
	}
	instanceID = strings.TrimSpace(instanceID)
	set.mu.RLock()
	instance := set.executors[instanceID]
	set.mu.RUnlock()
	if instance == nil {
		return Snapshot{}, ErrInstanceNotRegistered
	}
	return instance.snapshot(), nil
}

func (instance *executor) dispatch(ctx context.Context, input dispatchworker.Request) (dispatchworker.Result, error) {
	if err := instance.checkCircuit(time.Now()); err != nil {
		return dispatchworker.Result{}, err
	}
	select {
	case instance.slots <- struct{}{}:
		defer func() { <-instance.slots }()
	case <-ctx.Done():
		return dispatchworker.Result{}, ctx.Err()
	}
	if err := instance.checkCircuit(time.Now()); err != nil {
		return dispatchworker.Result{}, err
	}

	instance.mu.Lock()
	instance.inFlight++
	instance.mu.Unlock()
	defer func() {
		instance.mu.Lock()
		instance.inFlight--
		instance.mu.Unlock()
	}()

	requestContext, cancel := context.WithTimeout(ctx, instance.timeout)
	defer cancel()
	started := time.Now()
	result, err := instance.worker.Dispatch(requestContext, input)
	instance.record(result, err, time.Since(started), time.Now())
	return result, err
}

func (instance *executor) checkCircuit(now time.Time) error {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if !instance.openUntil.IsZero() && now.Before(instance.openUntil) {
		return ErrCircuitOpen
	}
	if !instance.openUntil.IsZero() && !now.Before(instance.openUntil) {
		instance.openUntil = time.Time{}
		instance.consecutiveFailures = 0
	}
	return nil
}

func (instance *executor) record(result dispatchworker.Result, err error, latency time.Duration, now time.Time) {
	failure := circuitFailure(result, err)
	instance.mu.Lock()
	defer instance.mu.Unlock()
	instance.totalDispatches++
	instance.lastLatency = latency
	if !failure {
		instance.consecutiveFailures = 0
		return
	}
	instance.failedDispatches++
	instance.consecutiveFailures++
	if instance.consecutiveFailures >= instance.failureThreshold {
		instance.openUntil = now.Add(instance.cooldown)
	}
}

func circuitFailure(result dispatchworker.Result, err error) bool {
	if result.Response.ResponseFormat == "transport_error" || result.Response.HTTPStatus >= 500 {
		return true
	}
	if err == nil || errors.Is(err, dispatchworker.ErrInvalidDispatchRequest) || errors.Is(err, dispatchworker.ErrDispatchAlreadyAttempted) {
		return false
	}
	return true
}

func (instance *executor) snapshot() Snapshot {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	now := time.Now()
	return Snapshot{
		InstanceID:          instance.instanceID,
		Open:                !instance.openUntil.IsZero() && now.Before(instance.openUntil),
		OpenUntil:           instance.openUntil,
		InFlight:            instance.inFlight,
		ConsecutiveFailures: instance.consecutiveFailures,
		TotalDispatches:     instance.totalDispatches,
		FailedDispatches:    instance.failedDispatches,
		LastLatency:         instance.lastLatency,
	}
}

func normalizeConfig(config Config) (Config, error) {
	if config.MaxInFlight <= 0 {
		config.MaxInFlight = 1
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 3
	}
	if config.Cooldown <= 0 {
		config.Cooldown = 15 * time.Second
	}
	return config, nil
}
