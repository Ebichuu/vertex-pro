package rules

import "fmt"

type Operator string

type FieldSpec struct {
	Name      string
	ValueType ValueType
	Operators []Operator
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
	{Name: "site", ValueType: StringValue, Operators: stringOperators()},
	{Name: "rss", ValueType: StringValue, Operators: stringOperators()},
	{Name: "title", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator)},
	{Name: "size", ValueType: NumberValue, Operators: numberOperators()},
	{Name: "category", ValueType: StringValue, Operators: stringOperators()},
	{Name: "published_at", ValueType: TimeValue, Operators: timeOperators()},
	{Name: "discovery_delay", ValueType: NumberValue, Operators: numberOperators()},
	{Name: "promotion", ValueType: BooleanValue, Operators: booleanOperators()},
	{Name: "tags", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "publisher", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator)},
	{Name: "group", ValueType: StringValue, Operators: append(stringOperators(), RegexOperator)},
	{Name: "qb_profile", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "instance_labels", ValueType: StringSetValue, Operators: setOperators()},
	{Name: "storage_labels", ValueType: StringSetValue, Operators: setOperators()},
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
		fields[index] = FieldSpec{Name: field.Name, ValueType: field.ValueType, Operators: append([]Operator(nil), field.Operators...)}
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
