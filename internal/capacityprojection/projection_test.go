package capacityprojection

import (
	"math"
	"testing"
)

func TestProjectAppliesAllDebitsExactlyOnce(t *testing.T) {
	result := Project(Input{PhysicalFreeBytes: 10_000, Entries: []LedgerEntry{
		{Key: "margin", Kind: KindSafetyMargin, Bytes: 500},
		{Key: "reservation:1", Kind: KindReservation, Bytes: 2_000},
		{Key: "torrent:1", Kind: KindUnmanagedLiability, Bytes: 1_250},
		{Key: "uncertainty:1", Kind: KindUncertaintyDebit, Bytes: 250},
	}})
	if result.State != StateTrusted || result.EffectiveFreeBytes == nil || *result.EffectiveFreeBytes != 6_000 {
		t.Fatalf("result = %#v, want trusted effective free 6000", result)
	}
	if result.TotalDebitedBytes != 4_000 {
		t.Fatalf("total debited = %d, want 4000", result.TotalDebitedBytes)
	}
}

func TestProjectFailsClosedForNegativeValuesAndOvercommit(t *testing.T) {
	cases := []struct {
		name   string
		input  Input
		reason string
	}{
		{name: "negative physical free", input: Input{PhysicalFreeBytes: -1}, reason: "PHYSICAL_FREE_NEGATIVE"},
		{name: "negative ledger bytes", input: Input{PhysicalFreeBytes: 100, Entries: []LedgerEntry{{Key: "bad", Kind: KindReservation, Bytes: -1}}}, reason: "LEDGER_BYTES_NEGATIVE"},
		{name: "debit exceeds physical free", input: Input{PhysicalFreeBytes: 100, Entries: []LedgerEntry{{Key: "reservation:1", Kind: KindReservation, Bytes: 101}}}, reason: "PROJECTED_LIABILITY_EXCEEDS_PHYSICAL_FREE"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Project(testCase.input)
			if result.State != StateUncertain {
				t.Fatalf("state = %q, want uncertain", result.State)
			}
			assertReason(t, result.Reasons, testCase.reason)
		})
	}
}

func TestProjectRejectsDuplicateLedgerKeyAcrossKinds(t *testing.T) {
	result := Project(Input{PhysicalFreeBytes: 1_000, Entries: []LedgerEntry{
		{Key: "torrent:1", Kind: KindReservation, Bytes: 100},
		{Key: "torrent:1", Kind: KindUnmanagedLiability, Bytes: 100},
	}})
	if result.State != StateUncertain {
		t.Fatalf("state = %q, want uncertain", result.State)
	}
	assertReason(t, result.Reasons, "LEDGER_KEY_DUPLICATE")
}

func TestProjectRejectsOverflowAndInvalidKinds(t *testing.T) {
	result := Project(Input{PhysicalFreeBytes: math.MaxInt64, Entries: []LedgerEntry{
		{Key: "a", Kind: KindReservation, Bytes: math.MaxInt64},
		{Key: "b", Kind: EntryKind("UNKNOWN"), Bytes: 1},
	}})
	if result.State != StateUncertain {
		t.Fatalf("state = %q, want uncertain", result.State)
	}
	assertReason(t, result.Reasons, "LEDGER_BYTES_OVERFLOW")
	assertReason(t, result.Reasons, "LEDGER_KIND_INVALID")
}

func TestProjectRejectsMissingLedgerIdentity(t *testing.T) {
	result := Project(Input{PhysicalFreeBytes: 100, Entries: []LedgerEntry{{Kind: KindReservation, Bytes: 1}}})
	if result.State != StateUncertain {
		t.Fatalf("state = %q, want uncertain", result.State)
	}
	assertReason(t, result.Reasons, "LEDGER_KEY_MISSING")
}

func assertReason(t *testing.T, reasons []string, expected string) {
	t.Helper()
	for _, reason := range reasons {
		if reason == expected {
			return
		}
	}
	t.Fatalf("reasons = %#v, missing %q", reasons, expected)
}
