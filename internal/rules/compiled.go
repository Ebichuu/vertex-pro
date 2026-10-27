package rules

import "regexp"

// Compiled is a condition tree validated against the catalog once, with its
// regex patterns precompiled. It is immutable and safe for concurrent use.
// Evaluation results, including reason codes, are identical to Evaluate and
// EvaluatePartial on the source tree.
type Compiled struct {
	root Node
}

// Compile validates tree and prepares it for repeated evaluation. An invalid
// regex is not a compile error: it evaluates to UNKNOWN/INVALID_REGEX exactly
// as Evaluate does.
func Compile(tree ConditionTree) (*Compiled, error) {
	if err := ValidateCatalog(tree); err != nil {
		return nil, err
	}
	return &Compiled{root: compileNode(tree.Root)}, nil
}

func compileNode(node Node) Node {
	if len(node.Children) > 0 {
		children := make([]Node, len(node.Children))
		for index, child := range node.Children {
			children[index] = compileNode(child)
		}
		node.Children = children
	}
	if node.Kind == LeafKind && node.Operator == RegexOperator && node.Value != nil && node.Value.String != nil {
		if pattern, err := regexp.Compile(*node.Value.String); err == nil {
			node.pattern = pattern
		}
	}
	return node
}

func (compiled *Compiled) Evaluate(facts Facts) Result {
	if facts == nil {
		facts = Facts{}
	}
	return evaluateNode(compiled.root, facts)
}

func (compiled *Compiled) EvaluatePartial(facts Facts) State {
	return partialNode(compiled.root, facts)
}
