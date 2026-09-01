package taskbinding

import "github.com/vertex-pro/vertex-pro/internal/amountleft"

type OwnerKind string

type ReservationState string

type UncertaintyDebitKind string

const (
	OwnerReservation OwnerKind = "RESERVATION"
	OwnerUnmanaged   OwnerKind = "UNMANAGED"

	ReservationActive     ReservationState = "ACTIVE"
	ReservationReflecting ReservationState = "REFLECTING"
	ReservationReleased   ReservationState = "RELEASED"

	DebitNone            UncertaintyDebitKind = "NONE"
	DebitAmountLeftBound UncertaintyDebitKind = "UNKNOWN_AMOUNT_LEFT_BOUND"
	DebitUnboundedAmount UncertaintyDebitKind = "UNBOUNDED_AMOUNT_LEFT"
)

type Reservation struct {
	ID              string
	StorageTargetID string
	State           ReservationState
}

type Binding struct {
	ID                    string
	StorageTargetID       string
	QBTorrentID           string
	OwnerKind             OwnerKind
	ReservationID         string
	AmountLeft            amountleft.Value
	UncertaintyDebitKind  UncertaintyDebitKind
	UncertaintyDebitBytes int64
}

type Result struct {
	Reasons []string
}

func Validate(binding Binding, reservations []Reservation, existing []Binding) Result {
	reasons := make([]string, 0)
	if binding.ID == "" {
		reasons = append(reasons, "BINDING_ID_MISSING")
	}
	if binding.StorageTargetID == "" {
		reasons = append(reasons, "STORAGE_TARGET_ID_MISSING")
	}
	if binding.QBTorrentID == "" {
		reasons = append(reasons, "QB_TORRENT_ID_MISSING")
	}
	if binding.UncertaintyDebitBytes < 0 {
		reasons = append(reasons, "UNCERTAINTY_DEBIT_NEGATIVE")
	}
	if duplicateBinding(binding, existing) {
		reasons = append(reasons, "QB_TORRENT_OWNER_DUPLICATE")
	}
	switch binding.OwnerKind {
	case OwnerReservation:
		validateReservationOwner(binding, reservations, &reasons)
	case OwnerUnmanaged:
		if binding.ReservationID != "" {
			reasons = append(reasons, "UNMANAGED_RESERVATION_ID_FORBIDDEN")
		}
	default:
		reasons = append(reasons, "OWNER_KIND_INVALID")
	}
	validateAmountLeft(binding, &reasons)
	return Result{Reasons: reasons}
}

func validateReservationOwner(binding Binding, reservations []Reservation, reasons *[]string) {
	if binding.ReservationID == "" {
		*reasons = append(*reasons, "RESERVATION_ID_REQUIRED")
		return
	}
	for _, reservation := range reservations {
		if reservation.ID != binding.ReservationID {
			continue
		}
		if reservation.StorageTargetID != binding.StorageTargetID {
			*reasons = append(*reasons, "RESERVATION_TARGET_MISMATCH")
		}
		if reservation.State != ReservationActive && reservation.State != ReservationReflecting {
			*reasons = append(*reasons, "RESERVATION_OWNER_NOT_ACTIVE")
		}
		return
	}
	*reasons = append(*reasons, "RESERVATION_NOT_FOUND")
}

func validateAmountLeft(binding Binding, reasons *[]string) {
	switch binding.AmountLeft.State {
	case amountleft.StateKnown:
		if binding.AmountLeft.Value == nil || *binding.AmountLeft.Value < 0 || binding.AmountLeft.ErrorCode != "" {
			*reasons = append(*reasons, "KNOWN_AMOUNT_LEFT_INVALID")
		}
		if binding.UncertaintyDebitKind != DebitNone || binding.UncertaintyDebitBytes != 0 {
			*reasons = append(*reasons, "KNOWN_AMOUNT_LEFT_CANNOT_HAVE_UNCERTAINTY_DEBIT")
		}
	case amountleft.StateUnknown:
		if binding.AmountLeft.Value != nil || binding.AmountLeft.ErrorCode == "" {
			*reasons = append(*reasons, "UNKNOWN_AMOUNT_LEFT_INVALID")
		}
		switch binding.OwnerKind {
		case OwnerReservation:
			if binding.UncertaintyDebitKind != DebitNone || binding.UncertaintyDebitBytes != 0 {
				*reasons = append(*reasons, "RESERVATION_UNKNOWN_AMOUNT_CANNOT_HAVE_DEBIT")
			}
		case OwnerUnmanaged:
			switch binding.UncertaintyDebitKind {
			case DebitAmountLeftBound:
				if binding.UncertaintyDebitBytes <= 0 {
					*reasons = append(*reasons, "UNKNOWN_AMOUNT_BOUND_REQUIRED")
				}
			case DebitUnboundedAmount:
				if binding.UncertaintyDebitBytes != 0 {
					*reasons = append(*reasons, "UNBOUNDED_AMOUNT_DEBIT_MUST_BE_ZERO")
				}
			default:
				*reasons = append(*reasons, "UNKNOWN_UNMANAGED_AMOUNT_REQUIRES_DEBIT_KIND")
			}
		}
	default:
		*reasons = append(*reasons, "AMOUNT_LEFT_STATE_INVALID")
	}
	if binding.AmountLeft.State == amountleft.StateKnown && binding.UncertaintyDebitKind != DebitNone {
		*reasons = append(*reasons, "KNOWN_AMOUNT_REQUIRES_NO_DEBIT")
	}
}

func duplicateBinding(binding Binding, existing []Binding) bool {
	for _, candidate := range existing {
		if candidate.StorageTargetID == binding.StorageTargetID && candidate.QBTorrentID == binding.QBTorrentID && candidate.ID != binding.ID {
			return true
		}
	}
	return false
}
