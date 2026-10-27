package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	CurrentVersion = 1
	MaxDepth       = 16
	MaxNodes       = 128
	MaxJSONBytes   = 64 * 1024
	MaxChildren    = 64
	MaxTextBytes   = 4096
)

type NodeKind string
type GroupOperator string
type ValueType string

const (
	GroupKind NodeKind = "group"
	LeafKind  NodeKind = "leaf"

	AndOperator = "AND"
	OrOperator  = "OR"

	StringValue      ValueType = "string"
	NumberValue      ValueType = "number"
	BooleanValue     ValueType = "boolean"
	StringSetValue   ValueType = "string_set"
	TimeValue        ValueType = "time"
	NumberSetValue   ValueType = "number_set"
	NumberRangeValue ValueType = "number_range"
	TimeSetValue     ValueType = "time_set"
	TimeRangeValue   ValueType = "time_range"
)

type ConditionTree struct {
	Version int  `json:"version"`
	Root    Node `json:"root"`
}

type Node struct {
	Kind     NodeKind `json:"kind"`
	Operator string   `json:"operator,omitempty"`
	Children []Node   `json:"children,omitempty"`
	Field    string   `json:"field,omitempty"`
	Value    *Value   `json:"value,omitempty"`
	// pattern is the precompiled regex of a regex leaf in a Compiled tree.
	pattern *regexp.Regexp
}

type Value struct {
	Type    ValueType `json:"type"`
	String  *string   `json:"string,omitempty"`
	Number  *float64  `json:"number,omitempty"`
	Boolean *bool     `json:"boolean,omitempty"`
	Strings []string  `json:"strings,omitempty"`
	Time    *string   `json:"time,omitempty"`
	Numbers []float64 `json:"numbers,omitempty"`
	Times   []string  `json:"times,omitempty"`
}

func Parse(data []byte) (ConditionTree, error) {
	if len(data) == 0 || len(data) > MaxJSONBytes {
		return ConditionTree{}, errors.New("condition tree JSON size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var tree ConditionTree
	if err := decoder.Decode(&tree); err != nil {
		return ConditionTree{}, fmt.Errorf("decode condition tree: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ConditionTree{}, errors.New("condition tree must contain one JSON value")
		}
		return ConditionTree{}, fmt.Errorf("decode trailing condition tree data: %w", err)
	}
	if err := tree.Validate(); err != nil {
		return ConditionTree{}, err
	}
	return tree, nil
}

func (tree ConditionTree) Validate() error {
	if tree.Version != CurrentVersion {
		return fmt.Errorf("unsupported condition tree version: %d", tree.Version)
	}
	count := 0
	return validateNode(tree.Root, 1, &count)
}

func validateNode(node Node, depth int, count *int) error {
	if depth > MaxDepth {
		return errors.New("condition tree depth exceeds limit")
	}
	*count++
	if *count > MaxNodes {
		return errors.New("condition tree node count exceeds limit")
	}
	switch node.Kind {
	case GroupKind:
		if node.Field != "" || node.Value != nil {
			return errors.New("group node cannot contain leaf fields")
		}
		if node.Operator != AndOperator && node.Operator != OrOperator {
			return errors.New("group operator is invalid")
		}
		if len(node.Children) == 0 || len(node.Children) > MaxChildren {
			return errors.New("group child count is invalid")
		}
		for _, child := range node.Children {
			if err := validateNode(child, depth+1, count); err != nil {
				return err
			}
		}
	case LeafKind:
		if node.Operator == "" || strings.TrimSpace(node.Field) == "" || len(node.Field) > MaxTextBytes {
			return errors.New("leaf field and operator are required")
		}
		if len(node.Children) != 0 {
			return errors.New("leaf node cannot contain children")
		}
		if node.Value == nil {
			if node.Operator != "exists" && node.Operator != "not_exists" {
				return errors.New("leaf value is required")
			}
			return nil
		}
		if err := node.Value.validate(); err != nil {
			return err
		}
	default:
		return errors.New("node kind is invalid")
	}
	return nil
}

func (value Value) validate() error {
	payloads := 0
	if value.String != nil {
		payloads++
	}
	if value.Number != nil {
		payloads++
	}
	if value.Boolean != nil {
		payloads++
	}
	if value.Strings != nil {
		payloads++
	}
	if value.Time != nil {
		payloads++
	}
	if value.Numbers != nil {
		payloads++
	}
	if value.Times != nil {
		payloads++
	}
	switch value.Type {
	case StringValue:
		if value.String == nil || payloads != 1 || len([]byte(*value.String)) > MaxTextBytes {
			return errors.New("string value payload is invalid")
		}
	case NumberValue:
		if value.Number == nil || payloads != 1 || math.IsNaN(*value.Number) || math.IsInf(*value.Number, 0) {
			return errors.New("number value payload is invalid")
		}
	case BooleanValue:
		if value.Boolean == nil || payloads != 1 {
			return errors.New("boolean value payload is invalid")
		}
	case StringSetValue:
		if value.Strings == nil || payloads != 1 || len(value.Strings) == 0 || len(value.Strings) > MaxChildren {
			return errors.New("string set value payload is invalid")
		}
		for _, item := range value.Strings {
			if item == "" || len([]byte(item)) > MaxTextBytes {
				return errors.New("string set item is invalid")
			}
		}
	case TimeValue:
		if value.Time == nil || payloads != 1 {
			return errors.New("time value payload is invalid")
		}
		if _, err := time.Parse(time.RFC3339, *value.Time); err != nil {
			return errors.New("time value must be RFC3339")
		}
	case NumberSetValue, NumberRangeValue:
		if value.Numbers == nil || payloads != 1 {
			return errors.New("number collection value payload is invalid")
		}
		if value.Type == NumberSetValue && (len(value.Numbers) == 0 || len(value.Numbers) > MaxChildren) {
			return errors.New("number set value payload is invalid")
		}
		if value.Type == NumberRangeValue && len(value.Numbers) != 2 {
			return errors.New("number range value payload is invalid")
		}
		for _, number := range value.Numbers {
			if math.IsNaN(number) || math.IsInf(number, 0) {
				return errors.New("number collection contains invalid number")
			}
		}
	case TimeSetValue, TimeRangeValue:
		if value.Times == nil || payloads != 1 {
			return errors.New("time collection value payload is invalid")
		}
		if value.Type == TimeSetValue && (len(value.Times) == 0 || len(value.Times) > MaxChildren) {
			return errors.New("time set value payload is invalid")
		}
		if value.Type == TimeRangeValue && len(value.Times) != 2 {
			return errors.New("time range value payload is invalid")
		}
		for _, item := range value.Times {
			if _, err := time.Parse(time.RFC3339, item); err != nil {
				return errors.New("time collection contains invalid time")
			}
		}
	default:
		return errors.New("value type is invalid")
	}
	return nil
}
