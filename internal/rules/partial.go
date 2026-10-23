package rules

// EvaluatePartial treats absent facts as unresolved, including existence
// operators. FALSE is definitive only with the supplied stable facts.
func EvaluatePartial(tree ConditionTree, facts Facts) (State, error) {
	if err := ValidateCatalog(tree); err != nil {
		return UnknownState, err
	}
	return partialNode(tree.Root, facts), nil
}

func partialNode(node Node, facts Facts) State {
	if node.Kind != GroupKind {
		if fact, ok := facts[node.Field]; !ok || !fact.Present {
			return UnknownState
		}
		return evaluateLeaf(node, facts).State
	}
	unknown := false
	for _, child := range node.Children {
		state := partialNode(child, facts)
		if node.Operator == AndOperator && state == FalseState {
			return FalseState
		}
		if node.Operator == OrOperator && state == TrueState {
			return TrueState
		}
		unknown = unknown || state == UnknownState
	}
	if unknown {
		return UnknownState
	}
	if node.Operator == AndOperator {
		return TrueState
	}
	return FalseState
}
