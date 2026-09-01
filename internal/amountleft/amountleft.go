package amountleft

import (
	"encoding/json"
	"math"
	"strconv"
)

type State string

type ErrorCode string

const (
	StateKnown   State = "KNOWN"
	StateUnknown State = "UNKNOWN"

	ErrorMissingInFullSnapshot ErrorCode = "MISSING_IN_FULL_SNAPSHOT"
	ErrorNegativeValue         ErrorCode = "NEGATIVE_VALUE"
	ErrorIntegerOverflow       ErrorCode = "INTEGER_OVERFLOW"
	ErrorTypeMismatch          ErrorCode = "TYPE_MISMATCH"
	ErrorNoTrustedPriorValue   ErrorCode = "NO_TRUSTED_PRIOR_VALUE"
	ErrorSchemaIncompatible    ErrorCode = "SCHEMA_INCOMPATIBLE"
)

type Value struct {
	State     State
	Value     *int64
	ErrorCode ErrorCode
}

type Observation struct {
	Present bool
	Raw     any
}

func ResolveFull(observation Observation) Value {
	if !observation.Present {
		return unknown(ErrorMissingInFullSnapshot)
	}
	return parse(observation.Raw)
}

func ResolveIncremental(observation Observation, prior Value) Value {
	if !observation.Present {
		if prior.State == StateKnown && prior.Value != nil && *prior.Value >= 0 {
			inherited := *prior.Value
			return Value{State: StateKnown, Value: &inherited}
		}
		return unknown(ErrorNoTrustedPriorValue)
	}
	return parse(observation.Raw)
}

func parse(raw any) Value {
	switch typed := raw.(type) {
	case int:
		return fromSigned(int64(typed))
	case int8:
		return fromSigned(int64(typed))
	case int16:
		return fromSigned(int64(typed))
	case int32:
		return fromSigned(int64(typed))
	case int64:
		return fromSigned(typed)
	case uint:
		return fromUnsigned(uint64(typed))
	case uint8:
		return fromUnsigned(uint64(typed))
	case uint16:
		return fromUnsigned(uint64(typed))
	case uint32:
		return fromUnsigned(uint64(typed))
	case uint64:
		return fromUnsigned(typed)
	case json.Number:
		value, err := strconv.ParseInt(string(typed), 10, 64)
		if err == nil {
			return fromSigned(value)
		}
		if _, floatErr := strconv.ParseFloat(string(typed), 64); floatErr == nil {
			return unknown(ErrorTypeMismatch)
		}
		return unknown(ErrorIntegerOverflow)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return unknown(ErrorTypeMismatch)
		}
		if typed < math.MinInt64 || typed > math.MaxInt64 {
			return unknown(ErrorIntegerOverflow)
		}
		return fromSigned(int64(typed))
	case float32:
		value := float64(typed)
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return unknown(ErrorTypeMismatch)
		}
		if value < math.MinInt64 || value > math.MaxInt64 {
			return unknown(ErrorIntegerOverflow)
		}
		return fromSigned(int64(typed))
	default:
		return unknown(ErrorTypeMismatch)
	}
}

func fromSigned(value int64) Value {
	if value < 0 {
		return unknown(ErrorNegativeValue)
	}
	return Value{State: StateKnown, Value: &value}
}

func fromUnsigned(value uint64) Value {
	if value > math.MaxInt64 {
		return unknown(ErrorIntegerOverflow)
	}
	converted := int64(value)
	return Value{State: StateKnown, Value: &converted}
}

func unknown(code ErrorCode) Value {
	return Value{State: StateUnknown, ErrorCode: code}
}
