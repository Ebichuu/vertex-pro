package raceprep

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestSupervisorHandlesAllSixCompletionOrders(t *testing.T) {
	orders := [][]Kind{{RuleKind, TorrentKind, PlacementKind}, {RuleKind, PlacementKind, TorrentKind}, {TorrentKind, RuleKind, PlacementKind}, {TorrentKind, PlacementKind, RuleKind}, {PlacementKind, RuleKind, TorrentKind}, {PlacementKind, TorrentKind, RuleKind}}
	for index, order := range orders {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			ready := make(chan Kind, 3)
			completed := make(chan Kind, 3)
			release := make(map[Kind]chan struct{}, 3)
			for _, kind := range []Kind{RuleKind, TorrentKind, PlacementKind} {
				release[kind] = make(chan struct{})
			}
			workers := Workers{Rule: waitWorker(RuleKind, ready, completed, release), Torrent: waitWorker(TorrentKind, ready, completed, release), Placement: waitWorker(PlacementKind, ready, completed, release)}
			var supervisor Supervisor
			resultCh := make(chan Result, 1)
			go func() {
				result, err := supervisor.Run(context.Background(), workers)
				if err != nil {
					t.Errorf("run error: %v", err)
				}
				resultCh <- result
			}()
			for range 3 {
				<-ready
			}
			for index, kind := range order {
				close(release[kind])
				if got := <-completed; got != kind {
					t.Fatalf("completion %d=%s want=%s", index, got, kind)
				}
			}
			result := <-resultCh
			if !result.Ready || len(result.Outcomes) != 3 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestSupervisorCapturesWorkerErrorAndPanicWithoutRunningReservation(t *testing.T) {
	var supervisor Supervisor
	result, err := supervisor.Run(context.Background(), Workers{Rule: func(context.Context) error { return errors.New("rejected") }, Torrent: func(context.Context) error { panic("boom") }, Placement: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Outcomes[RuleKind].Err == nil || !result.Outcomes[TorrentKind].Panicked || result.Outcomes[PlacementKind].Err != nil {
		t.Fatalf("result=%+v", result)
	}
}

func TestSupervisorPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var supervisor Supervisor
	result, err := supervisor.Run(ctx, Workers{Rule: func(context.Context) error { return nil }, Torrent: func(context.Context) error { return nil }, Placement: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready {
		t.Fatalf("result=%+v", result)
	}
	for _, outcome := range result.Outcomes {
		if !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("outcome=%+v", outcome)
		}
	}
}

func TestSupervisorRejectsConcurrentRunAndMissingWorker(t *testing.T) {
	var supervisor Supervisor
	started := make(chan struct{})
	release := make(chan struct{})
	workers := Workers{Rule: func(context.Context) error { close(started); <-release; return nil }, Torrent: func(context.Context) error { <-release; return nil }, Placement: func(context.Context) error { <-release; return nil }}
	first := make(chan error, 1)
	go func() { _, err := supervisor.Run(context.Background(), workers); first <- err }()
	<-started
	if _, err := supervisor.Run(context.Background(), workers); err == nil {
		t.Fatal("expected concurrent run rejection")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Run(context.Background(), Workers{}); err == nil {
		t.Fatal("expected missing worker error")
	}
}

func TestSupervisorReturnsStableOutcomeSetDespiteCompletionOrder(t *testing.T) {
	var supervisor Supervisor
	result, err := supervisor.Run(context.Background(), Workers{Rule: func(context.Context) error { time.Sleep(5 * time.Millisecond); return nil }, Torrent: func(context.Context) error { return nil }, Placement: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(result.Outcomes))
	for kind := range result.Outcomes {
		keys = append(keys, string(kind))
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"placement", "rule", "torrent"}) {
		t.Fatalf("keys=%v", keys)
	}
}

func waitWorker(kind Kind, ready chan<- Kind, completed chan<- Kind, release map[Kind]chan struct{}) Worker {
	return func(context.Context) error {
		ready <- kind
		<-release[kind]
		completed <- kind
		return nil
	}
}
