package github

import (
	"gopkg.in/yaml.v3"
)

// hasWorkflowDispatch reports whether a workflow file declares the
// workflow_dispatch trigger. The "on" key accepts a string, a list or a map.
// yaml.v3 nodes are used so "on" is never coerced to a boolean.
func hasWorkflowDispatch(data []byte) (bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	if len(doc.Content) == 0 {
		return false, nil
	}
	root := deref(doc.Content[0])
	if root.Kind != yaml.MappingNode {
		return false, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "on" {
			continue
		}
		on := deref(root.Content[i+1])
		switch on.Kind {
		case yaml.ScalarNode:
			return on.Value == "workflow_dispatch", nil
		case yaml.SequenceNode:
			for _, n := range on.Content {
				if deref(n).Value == "workflow_dispatch" {
					return true, nil
				}
			}
		case yaml.MappingNode:
			for j := 0; j < len(on.Content); j += 2 {
				if on.Content[j].Value == "workflow_dispatch" {
					return true, nil
				}
			}
		}
		return false, nil
	}
	return false, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}
