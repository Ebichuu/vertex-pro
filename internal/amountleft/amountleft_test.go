package amountleft

import (
	"encoding/json"
	"math"
	"testing"
)

func TestResolveFullAcceptsZeroAndPositiveInteger(t *testing.T) {
	for _, raw := range []any{int64(0), int64(512), json.Number("2048")} {
		result := ResolveFull(Observation{Present: true, Raw: raw})
		if result.State != StateKnown || result.Value == nil {
			t.Fatalf("result = %#v, want known", result)
		}
	}
}

func TestResolveFullMapsInvalidValuesToUnknown(t *testing.T) {
	cases := []struct {
		name   string
		raw    any
		reason ErrorCode
	}{
		{name: "missing", raw: nil, reason: ErrorMissingInFullSnapshot},
		{name: "negative", raw: int64(-1), reason: ErrorNegativeValue},
		{name: "overflow", raw: uint64(math.MaxInt64) + 1, reason: ErrorIntegerOverflow},
		{name: "fractional", raw: 1.5, reason: ErrorTypeMismatch},
		{name: "string", raw: "1024", reason: ErrorTypeMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := Observation{Present: true, Raw: testCase.raw}
			if testCase.name == "missing" {
				observation.Present = false
			}
			result := ResolveFull(observation)
			if result.State != StateUnknown || result.Value != nil || result.ErrorCode != testCase.reason {
				t.Fatalf("result = %#v, want unknown %q", result, testCase.reason)
			}
		})
	}
}

func TestResolveIncrementalInheritsOnlyTrustedPriorValue(t *testing.T) {
	prior := ResolveFull(Observation{Present: true, Raw: int64(2048)})
	inherited := ResolveIncremental(Observation{Present: false}, prior)
	if inherited.State != StateKnown || inherited.Value == nil || *inherited.Value != 2048 {
		t.Fatalf("inherited = %#v, want known 2048", inherited)
	}

	unknownPrior := ResolveFull(Observation{Present: true, Raw: int64(-1)})
	rejected := ResolveIncremental(Observation{Present: false}, unknownPrior)
	if rejected.State != StateUnknown || rejected.ErrorCode != ErrorNoTrustedPriorValue {
		t.Fatalf("rejected = %#v, want no trusted prior", rejected)
	}
}

func TestResolveFullDoesNotInheritPriorWhenFieldIsMissing(t *testing.T) {
	result := ResolveFull(Observation{Present: false})
	if result.State != StateUnknown || result.ErrorCode != ErrorMissingInFullSnapshot {
		t.Fatalf("result = %#v, want missing full snapshot", result)
	}
}

func TestResolveIncrementalPresentInvalidValueDoesNotInherit(t *testing.T) {
	prior := ResolveFull(Observation{Present: true, Raw: int64(2048)})
	result := ResolveIncremental(Observation{Present: true, Raw: "invalid"}, prior)
	if result.State != StateUnknown || result.ErrorCode != ErrorTypeMismatch {
		t.Fatalf("result = %#v, want type mismatch", result)
	}
}
