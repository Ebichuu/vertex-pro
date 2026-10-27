package rules

import (
	"fmt"
	"slices"
)

type Operator string

type FieldSpec struct {
	Name      string
	ValueType ValueType
	Operators []Operator
	// Live reports whether production matching supplies this fact. Other
	// fields evaluate only in dry-run with caller-supplied facts; in a saved
	// rule they would always be UNKNOWN and the rule could never match.
	Live bool
}

const (
	EqualsOperator         = "equals"
	NotEqualsOperator      = "not_equals"
	ContainsOperator       = "contains"
	NotContainsOperator    = "not_contains"
	RegexOperator          = "regex"
	GreaterThanOperator    = "greater_than"
	GreaterOrEqualOperator = "greater_or_equal"
	LessThanOperator       = "less_than"
	LessOrEqualOperator    = "less_or_equal"
	BetweenOperator        = "between"
	InOperator             = "in"
	NotInOperator          = "not_in"
	ExistsOperator         = "exists"
	NotExistsOperator      = "not_exists"
)

var fieldCatalog = []FieldSpec{
	{Name: "site", ValueType: StringValue, Operators: stringOperators(), Live: true},
	{Name: "rss", ValueType: StringValue, Operators: stringOperators(), Live: true},
	{Name: "title", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator), Live: true},
	{Name: "size", ValueType: NumberValue, Operators: numberOperators(), Live: true},
	{Name: "category", ValueType: StringValue, Operators: stringOperators(), Live: true},
	{Name: "published_at", ValueType: TimeValue, Operators: timeOperators(), Live: true},
	{Name: "discovery_delay", ValueType: NumberValue, Operators: numberOperators(), Live: true},
	// Needs a site driver that reads the torrent page; RSS does not carry it.
	{Name: "promotion", ValueType: BooleanValue, Operators: booleanOperators()},
	{Name: "tags", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "publisher", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator), Live: true},
	{Name: "group", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator), Live: true},
	// Placement attributes of the target, not facts of the torrent.
	{Name: "qb_profile", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "instance_labels", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "storage_labels", ValueType: StringSetValue, Operators: setOperators()},
}

var fieldTypes = func() map[string]ValueType {
	types := make(map[string]ValueType, len(fieldCatalog))
	for _, field := range fieldCatalog {
		types[field.Name] = field.ValueType
	}
	return types
}()

// UnavailableFields lists the distinct fields in tree that production
// matching never supplies, in first-use order.
func UnavailableFields(tree ConditionTree) []string {
	var fields []string
	var walk func(Node)
	walk = func(node Node) {
		for _, child := range node.Children {
			walk(child)
		}
		if node.Kind != LeafKind {
			return
		}
		if field, ok := LookupField(node.Field); ok && !field.Live && !slices.Contains(fields, node.Field) {
			fields = append(fields, node.Field)
		}
	}
	walk(tree.Root)
	return fields
}

func stringOperators() []Operator {
	return []Operator{EqualsOperator, NotEqualsOperator, ContainsOperator, NotContainsOperator, InOperator, NotInOperator, ExistsOperator, NotExistsOperator}
}
func numberOperators() []Operator {
	return []Operator{EqualsOperator, NotEqualsOperator, GreaterThanOperator, GreaterOrEqualOperator, LessThanOperator, LessOrEqualOperator, BetweenOperator, InOperator, NotInOperator, ExistsOperator, NotExistsOperator}
}
func timeOperators() []Operator {
	return []Operator{EqualsOperator, NotEqualsOperator, GreaterThanOperator, GreaterOrEqualOperator, LessThanOperator, LessOrEqualOperator, BetweenOperator, ExistsOperator, NotExistsOperator}
}
func booleanOperators() []Operator {
	return []Operator{EqualsOperator, NotEqualsOperator, ExistsOperator, NotExistsOperator}
}
func setOperators() []Operator {
	return []Operator{ContainsOperator, NotContainsOperator, ExistsOperator, NotExistsOperator}
}

func Fields() []FieldSpec {
	fields := make([]FieldSpec, len(fieldCatalog))
	for index, field := range fieldCatalog {
		fields[index] = FieldSpec{Name: field.Name, ValueType: field.ValueType, Operators: append([]Operator(nil), field.Operators...), Live: field.Live}
	}
	return fields
}

func LookupField(name string) (FieldSpec, bool) {
	for _, field := range fieldCatalog {
		if field.Name == name {
			field.Operators = append([]Operator(nil), field.Operators...)
			return field, true
		}
	}
	return FieldSpec{}, false
}

func ValidateCatalog(tree ConditionTree) error {
	if err := tree.Validate(); err != nil {
		return err
	}
	return validateCatalogNode(tree.Root)
}

func validateCatalogNode(node Node) error {
	if node.Kind == GroupKind {
		for _, child := range node.Children {
			if err := validateCatalogNode(child); err != nil {
				return err
			}
		}
		return nil
	}
	field, ok := LookupField(node.Field)
	if !ok {
		return fmt.Errorf("unknown rule field: %s", node.Field)
	}
	operator := Operator(node.Operator)
	if !containsOperator(field.Operators, operator) {
		return fmt.Errorf("operator %s is not supported for field %s", operator, field.Name)
	}
	if operator == ExistsOperator || operator == NotExistsOperator {
		return nil
	}
	if node.Value == nil {
		return fmt.Errorf("rule value is required for field %s", field.Name)
	}
	if !compatibleValueType(field.ValueType, operator, node.Value.Type) {
		return fmt.Errorf("rule value type does not match field %s", field.Name)
	}
	return nil
}

func compatibleValueType(fieldType ValueType, operator Operator, valueType ValueType) bool {
	if operator == InOperator || operator == NotInOperator {
		switch fieldType {
		case StringValue:
			return valueType == StringSetValue
		case NumberValue:
			return valueType == NumberSetValue
		case TimeValue:
			return valueType == TimeSetValue
		}
	}
	if operator == BetweenOperator {
		switch fieldType {
		case NumberValue:
			return valueType == NumberRangeValue
		case TimeValue:
			return valueType == TimeRangeValue
		}
	}
	if fieldType == StringSetValue && (operator == ContainsOperator || operator == NotContainsOperator) {
		return valueType == StringValue
	}
	return fieldType == valueType
}

func containsOperator(operators []Operator, want Operator) bool {
	for _, operator := range operators {
		if operator == want {
			return true
		}
	}
	return false
}
