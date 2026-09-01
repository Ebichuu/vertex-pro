package taskbinding

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/amountleft"
)

func TestValidateAcceptsReservationOwnerOnSameActiveTarget(t *testing.T) {
	known := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(0)})
	result := Validate(Binding{ID: "binding-1", StorageTargetID: "target-1", QBTorrentID: "torrent-1", OwnerKind: OwnerReservation, ReservationID: "reservation-1", AmountLeft: known, UncertaintyDebitKind: DebitNone}, []Reservation{{ID: "reservation-1", StorageTargetID: "target-1", State: ReservationActive}}, nil)
	if len(result.Reasons) != 0 {
		t.Fatalf("reasons = %#v, want none", result.Reasons)
	}
}

func TestValidateRejectsMissingInactiveAndCrossTargetReservation(t *testing.T) {
	known := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(100)})
	cases := []struct {
		name   string
		state  ReservationState
		target string
		reason string
	}{
		{name: "missing", state: ReservationActive, target: "target-1", reason: "RESERVATION_NOT_FOUND"},
		{name: "released", state: ReservationReleased, target: "target-1", reason: "RESERVATION_OWNER_NOT_ACTIVE"},
		{name: "cross target", state: ReservationActive, target: "target-2", reason: "RESERVATION_TARGET_MISMATCH"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reservations := []Reservation(nil)
			if testCase.name != "missing" {
				reservations = []Reservation{{ID: "reservation-1", StorageTargetID: testCase.target, State: testCase.state}}
			}
			result := Validate(Binding{ID: "binding-1", StorageTargetID: "target-1", QBTorrentID: "torrent-1", OwnerKind: OwnerReservation, ReservationID: "reservation-1", AmountLeft: known, UncertaintyDebitKind: DebitNone}, reservations, nil)
			assertReason(t, result.Reasons, testCase.reason)
		})
	}
}

func TestValidateEnforcesUnmanagedOwnerAndUnknownAmountRules(t *testing.T) {
	unknown := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(-1)})
	invalidReservationID := Validate(Binding{ID: "binding-1", StorageTargetID: "target-1", QBTorrentID: "torrent-1", OwnerKind: OwnerUnmanaged, ReservationID: "reservation-1", AmountLeft: unknown, UncertaintyDebitKind: DebitUnboundedAmount}, nil, nil)
	assertReason(t, invalidReservationID.Reasons, "UNMANAGED_RESERVATION_ID_FORBIDDEN")

	validBound := Validate(Binding{ID: "binding-2", StorageTargetID: "target-1", QBTorrentID: "torrent-2", OwnerKind: OwnerUnmanaged, AmountLeft: unknown, UncertaintyDebitKind: DebitAmountLeftBound, UncertaintyDebitBytes: 512}, nil, nil)
	if len(validBound.Reasons) != 0 {
		t.Fatalf("valid bound reasons = %#v, want none", validBound.Reasons)
	}

	invalidKnown := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(100)})
	invalidDebit := Validate(Binding{ID: "binding-3", StorageTargetID: "target-1", QBTorrentID: "torrent-3", OwnerKind: OwnerUnmanaged, AmountLeft: invalidKnown, UncertaintyDebitKind: DebitAmountLeftBound, UncertaintyDebitBytes: 1}, nil, nil)
	assertReason(t, invalidDebit.Reasons, "KNOWN_AMOUNT_LEFT_CANNOT_HAVE_UNCERTAINTY_DEBIT")
}

func TestValidateRejectsDuplicateOwnerOnSameTargetAndTorrent(t *testing.T) {
	known := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(100)})
	existing := []Binding{{ID: "binding-1", StorageTargetID: "target-1", QBTorrentID: "torrent-1", OwnerKind: OwnerUnmanaged, AmountLeft: known, UncertaintyDebitKind: DebitNone}}
	result := Validate(Binding{ID: "binding-2", StorageTargetID: "target-1", QBTorrentID: "torrent-1", OwnerKind: OwnerUnmanaged, AmountLeft: known, UncertaintyDebitKind: DebitNone}, nil, existing)
	assertReason(t, result.Reasons, "QB_TORRENT_OWNER_DUPLICATE")
}

func TestValidateRejectsInvalidUnknownAmountCombinations(t *testing.T) {
	unknown := amountleft.ResolveFull(amountleft.Observation{Present: true, Raw: int64(-1)})
	cases := []struct {
		name   string
		kind   UncertaintyDebitKind
		bytes  int64
		reason string
	}{
		{name: "missing debit kind", kind: DebitNone, bytes: 0, reason: "UNKNOWN_UNMANAGED_AMOUNT_REQUIRES_DEBIT_KIND"},
		{name: "bound debit missing bytes", kind: DebitAmountLeftBound, bytes: 0, reason: "UNKNOWN_AMOUNT_BOUND_REQUIRED"},
		{name: "unbounded debit nonzero", kind: DebitUnboundedAmount, bytes: 1, reason: "UNBOUNDED_AMOUNT_DEBIT_MUST_BE_ZERO"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Validate(Binding{ID: "binding-1", StorageTargetID: "target-1", QBTorrentID: testCase.name, OwnerKind: OwnerUnmanaged, AmountLeft: unknown, UncertaintyDebitKind: testCase.kind, UncertaintyDebitBytes: testCase.bytes}, nil, nil)
			assertReason(t, result.Reasons, testCase.reason)
		})
	}
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
