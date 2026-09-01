package statsdelta

import (
	"testing"
)

func TestFirstCounterEstablishesBaselineWithoutDelta(t *testing.T) {
	outcome, err := Advance(Counter{}, false, Counter{Downloaded: 1000, Uploaded: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != KindBaseline || outcome.Delta.Downloaded != 0 || outcome.Delta.Uploaded != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Next.Downloaded != 1000 {
		t.Fatalf("baseline = %+v", outcome.Next)
	}
}

func TestNormalDelta(t *testing.T) {
	outcome, err := Advance(Counter{Downloaded: 1000, Uploaded: 2000}, true, Counter{Downloaded: 1500, Uploaded: 2600})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != KindDelta || outcome.Delta.Downloaded != 500 || outcome.Delta.Uploaded != 600 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestCounterResetProducesZeroDeltaAndNewBaseline(t *testing.T) {
	// qB 重启导致 all-time 计数回退：delta 为 0，绝不产生负流量。
	outcome, err := Advance(Counter{Downloaded: 5000, Uploaded: 8000}, true, Counter{Downloaded: 300, Uploaded: 400})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != KindReset || outcome.Delta.Downloaded != 0 || outcome.Delta.Uploaded != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Next.Downloaded != 300 {
		t.Fatalf("reset baseline = %+v", outcome.Next)
	}
}

func TestNegativeCountersRejected(t *testing.T) {
	if _, err := Advance(Counter{}, false, Counter{Downloaded: -1}); err == nil {
		t.Fatal("negative current error = nil")
	}
	if _, err := Advance(Counter{Downloaded: -5}, true, Counter{Downloaded: 1}); err == nil {
		t.Fatal("negative prev error = nil")
	}
}
