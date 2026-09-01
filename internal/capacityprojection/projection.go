package capacityprojection

import "math"

type State string

const (
	StateTrusted   State = "TRUSTED"
	StateUncertain State = "CAPACITY_UNCERTAIN"
)

type EntryKind string

const (
	KindSafetyMargin       EntryKind = "SAFETY_MARGIN"
	KindReservation        EntryKind = "ACTIVE_RESERVATION"
	KindUnmanagedLiability EntryKind = "UNMANAGED_LIABILITY"
	KindUncertaintyDebit   EntryKind = "UNCERTAINTY_DEBIT"
)

type LedgerEntry struct {
	Key   string
	Kind  EntryKind
	Bytes int64
}

type Input struct {
	PhysicalFreeBytes int64
	Entries           []LedgerEntry
}

type Result struct {
	State              State
	EffectiveFreeBytes *int64
	TotalDebitedBytes  int64
	Reasons            []string
}

func Project(input Input) Result {
	reasons := make([]string, 0)
	if input.PhysicalFreeBytes < 0 {
		reasons = append(reasons, "PHYSICAL_FREE_NEGATIVE")
	}
	seenKeys := make(map[string]struct{}, len(input.Entries))
	total := int64(0)
	for _, entry := range input.Entries {
		if entry.Key == "" {
			reasons = append(reasons, "LEDGER_KEY_MISSING")
		}
		if !validKind(entry.Kind) {
			reasons = append(reasons, "LEDGER_KIND_INVALID")
		}
		if entry.Bytes < 0 {
			reasons = append(reasons, "LEDGER_BYTES_NEGATIVE")
		}
		if _, exists := seenKeys[entry.Key]; exists && entry.Key != "" {
			reasons = append(reasons, "LEDGER_KEY_DUPLICATE")
		}
		if entry.Key != "" {
			seenKeys[entry.Key] = struct{}{}
		}
		if entry.Bytes >= 0 && total > math.MaxInt64-entry.Bytes {
			reasons = append(reasons, "LEDGER_BYTES_OVERFLOW")
		} else if entry.Bytes >= 0 {
			total += entry.Bytes
		}
	}
	if input.PhysicalFreeBytes >= 0 && total > input.PhysicalFreeBytes {
		reasons = append(reasons, "PROJECTED_LIABILITY_EXCEEDS_PHYSICAL_FREE")
	}
	if len(reasons) > 0 {
		return Result{State: StateUncertain, TotalDebitedBytes: total, Reasons: reasons}
	}
	effective := input.PhysicalFreeBytes - total
	return Result{State: StateTrusted, EffectiveFreeBytes: &effective, TotalDebitedBytes: total}
}

func validKind(kind EntryKind) bool {
	switch kind {
	case KindSafetyMargin, KindReservation, KindUnmanagedLiability, KindUncertaintyDebit:
		return true
	default:
		return false
	}
}
