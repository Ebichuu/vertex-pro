package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type State string

const (
	TrueState    State = "TRUE"
	FalseState   State = "FALSE"
	UnknownState State = "UNKNOWN"
)

type Fact struct {
	Present bool
	Value   Value
}

type Facts map[string]Fact

type Result struct {
	State  State
	Reason Reason
}

type Reason struct {
	State    State
	Code     string
	Field    string
	Operator string
	Children []Reason
}

func Evaluate(tree ConditionTree, facts Facts) (Result, error) {
	if err := ValidateCatalog(tree); err != nil {
		return Result{}, err
	}
	if facts == nil {
		facts = Facts{}
	}
	return evaluateNode(tree.Root, facts), nil
}

func evaluateNode(node Node, facts Facts) Result {
	if node.Kind == GroupKind {
		return evaluateGroup(node, facts)
	}
	return evaluateLeaf(node, facts)
}

func evaluateGroup(node Node, facts Facts) Result {
	children := make([]Reason, 0, len(node.Children))
	if node.Operator == AndOperator {
		for _, child := range node.Children {
			result := evaluateNode(child, facts)
			children = append(children, result.Reason)
			if result.State == FalseState {
				return Result{State: FalseState, Reason: Reason{State: FalseState, Code: "AND_FALSE", Operator: node.Operator, Children: children}}
			}
		}
		for _, child := range children {
			if child.State == UnknownState {
				return Result{State: UnknownState, Reason: Reason{State: UnknownState, Code: "AND_UNKNOWN", Operator: node.Operator, Children: children}}
			}
		}
		return Result{State: TrueState, Reason: Reason{State: TrueState, Code: "AND_TRUE", Operator: node.Operator, Children: children}}
	}
	for _, child := range node.Children {
		result := evaluateNode(child, facts)
		children = append(children, result.Reason)
		if result.State == TrueState {
			return Result{State: TrueState, Reason: Reason{State: TrueState, Code: "OR_TRUE", Operator: node.Operator, Children: children}}
		}
	}
	for _, child := range children {
		if child.State == UnknownState {
			return Result{State: UnknownState, Reason: Reason{State: UnknownState, Code: "OR_UNKNOWN", Operator: node.Operator, Children: children}}
		}
	}
	return Result{State: FalseState, Reason: Reason{State: FalseState, Code: "OR_FALSE", Operator: node.Operator, Children: children}}
}

func evaluateLeaf(node Node, facts Facts) Result {
	fact, present := facts[node.Field]
	if node.Operator == ExistsOperator || node.Operator == NotExistsOperator {
		state := FalseState
		code := "FIELD_ABSENT"
		if present && fact.Present {
			state = TrueState
			code = "FIELD_PRESENT"
		}
		if node.Operator == NotExistsOperator {
			state = invertState(state)
			if state == TrueState {
				code = "FIELD_ABSENT"
			} else {
				code = "FIELD_PRESENT"
			}
		}
		return leafResult(state, code, node)
	}
	if !present || !fact.Present {
		return leafResult(UnknownState, "FIELD_MISSING", node)
	}
	field, _ := LookupField(node.Field)
	if fact.Value.Type != field.ValueType {
		return leafResult(UnknownState, "FACT_TYPE_MISMATCH", node)
	}
	state, code := compareValues(field.ValueType, node.Operator, fact.Value, *node.Value)
	return leafResult(state, code, node)
}

func leafResult(state State, code string, node Node) Result {
	return Result{State: state, Reason: Reason{State: state, Code: code, Field: node.Field, Operator: node.Operator}}
}

func compareValues(fieldType ValueType, operator string, fact Value, wanted Value) (State, string) {
	switch fieldType {
	case StringValue:
		return compareString(operator, *fact.String, wanted)
	case StringSetValue:
		return compareStringSet(operator, fact.Strings, wanted)
	case NumberValue:
		return compareNumber(operator, *fact.Number, wanted)
	case BooleanValue:
		if operator != EqualsOperator && operator != NotEqualsOperator {
			return UnknownState, "UNSUPPORTED_BOOLEAN_OPERATOR"
		}
		if wanted.Boolean == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		equal := *fact.Boolean == *wanted.Boolean
		if operator == NotEqualsOperator {
			equal = !equal
		}
		if equal {
			return TrueState, "VALUE_MATCH"
		}
		return FalseState, "VALUE_MISMATCH"
	case TimeValue:
		return compareTime(operator, *fact.Time, wanted)
	default:
		return UnknownState, "UNSUPPORTED_FIELD_TYPE"
	}
}

func compareString(operator, fact string, wanted Value) (State, string) {
	switch operator {
	case EqualsOperator, NotEqualsOperator, ContainsOperator, NotContainsOperator, RegexOperator:
		if wanted.String == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
	case InOperator, NotInOperator:
		if wanted.Type != StringSetValue {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
	}
	switch operator {
	case EqualsOperator:
		return boolResult(fact == *wanted.String)
	case NotEqualsOperator:
		return boolResult(fact != *wanted.String)
	case ContainsOperator:
		return boolResult(strings.Contains(fact, *wanted.String))
	case NotContainsOperator:
		return boolResult(!strings.Contains(fact, *wanted.String))
	case RegexOperator:
		pattern, err := regexp.Compile(*wanted.String)
		if err != nil {
			return UnknownState, "INVALID_REGEX"
		}
		return boolResult(pattern.MatchString(fact))
	case InOperator, NotInOperator:
		matched := containsString(wanted.Strings, fact)
		if operator == NotInOperator {
			matched = !matched
		}
		return boolResult(matched)
	default:
		return UnknownState, "UNSUPPORTED_STRING_OPERATOR"
	}
}

func compareStringSet(operator string, fact []string, wanted Value) (State, string) {
	if wanted.String == nil {
		return UnknownState, "VALUE_TYPE_MISMATCH"
	}
	matched := containsString(fact, *wanted.String)
	if operator == NotContainsOperator {
		matched = !matched
	} else if operator != ContainsOperator {
		return UnknownState, "UNSUPPORTED_SET_OPERATOR"
	}
	return boolResult(matched)
}

func compareNumber(operator string, fact float64, wanted Value) (State, string) {
	switch operator {
	case EqualsOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact == *wanted.Number)
	case NotEqualsOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact != *wanted.Number)
	case GreaterThanOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact > *wanted.Number)
	case GreaterOrEqualOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact >= *wanted.Number)
	case LessThanOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact < *wanted.Number)
	case LessOrEqualOperator:
		if wanted.Number == nil {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact <= *wanted.Number)
	case BetweenOperator:
		if len(wanted.Numbers) != 2 {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		return boolResult(fact >= wanted.Numbers[0] && fact <= wanted.Numbers[1])
	case InOperator, NotInOperator:
		matched := false
		for _, number := range wanted.Numbers {
			if fact == number {
				matched = true
				break
			}
		}
		if operator == NotInOperator {
			matched = !matched
		}
		return boolResult(matched)
	default:
		return UnknownState, "UNSUPPORTED_NUMBER_OPERATOR"
	}
}

func compareTime(operator string, fact string, wanted Value) (State, string) {
	factTime, err := time.Parse(time.RFC3339, fact)
	if err != nil {
		return UnknownState, "INVALID_FACT_TIME"
	}
	if operator == BetweenOperator {
		if len(wanted.Times) != 2 {
			return UnknownState, "VALUE_TYPE_MISMATCH"
		}
		start, startErr := time.Parse(time.RFC3339, wanted.Times[0])
		end, endErr := time.Parse(time.RFC3339, wanted.Times[1])
		if startErr != nil || endErr != nil {
			return UnknownState, "INVALID_VALUE_TIME"
		}
		return boolResult(!factTime.Before(start) && !factTime.After(end))
	}
	if operator == InOperator || operator == NotInOperator {
		matched := containsString(wanted.Times, fact)
		if operator == NotInOperator {
			matched = !matched
		}
		return boolResult(matched)
	}
	if wanted.Time == nil {
		return UnknownState, "VALUE_TYPE_MISMATCH"
	}
	wantedTime, err := time.Parse(time.RFC3339, *wanted.Time)
	if err != nil {
		return UnknownState, "INVALID_VALUE_TIME"
	}
	switch operator {
	case EqualsOperator:
		return boolResult(factTime.Equal(wantedTime))
	case NotEqualsOperator:
		return boolResult(!factTime.Equal(wantedTime))
	case GreaterThanOperator:
		return boolResult(factTime.After(wantedTime))
	case GreaterOrEqualOperator:
		return boolResult(!factTime.Before(wantedTime))
	case LessThanOperator:
		return boolResult(factTime.Before(wantedTime))
	case LessOrEqualOperator:
		return boolResult(!factTime.After(wantedTime))
	default:
		return UnknownState, "UNSUPPORTED_TIME_OPERATOR"
	}
}

func boolResult(value bool) (State, string) {
	if value {
		return TrueState, "VALUE_MATCH"
	}
	return FalseState, "VALUE_MISMATCH"
}

func invertState(state State) State {
	if state == TrueState {
		return FalseState
	}
	if state == FalseState {
		return TrueState
	}
	return UnknownState
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (result Result) String() string {
	return fmt.Sprintf("%s:%s", result.State, result.Reason.Code)
}
