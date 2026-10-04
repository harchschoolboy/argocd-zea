package github

import (
	"gopkg.in/yaml.v3"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// hasWorkflowDispatch reports whether a workflow file declares the
// workflow_dispatch trigger. The "on" key accepts a string, a list or a map.
// yaml.v3 nodes are used so "on" is never coerced to a boolean.
func hasWorkflowDispatch(data []byte) (bool, error) {
	ok, _, err := parseDispatch(data)
	return ok, err
}

// parseDispatch returns whether the workflow declares workflow_dispatch and
// its inputs in declaration order.
func parseDispatch(data []byte) (bool, []providers.Input, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, nil, err
	}
	if len(doc.Content) == 0 {
		return false, nil, nil
	}
	on := mapValue(deref(doc.Content[0]), "on")
	if on == nil {
		return false, nil, nil
	}
	switch on.Kind {
	case yaml.ScalarNode:
		return on.Value == "workflow_dispatch", nil, nil
	case yaml.SequenceNode:
		for _, n := range on.Content {
			if deref(n).Value == "workflow_dispatch" {
				return true, nil, nil
			}
		}
	case yaml.MappingNode:
		for j := 0; j+1 < len(on.Content); j += 2 {
			if on.Content[j].Value == "workflow_dispatch" {
				return true, dispatchInputs(deref(on.Content[j+1])), nil
			}
		}
	}
	return false, nil, nil
}

func dispatchInputs(wd *yaml.Node) []providers.Input {
	inputs := mapValue(wd, "inputs")
	if inputs == nil || inputs.Kind != yaml.MappingNode {
		return nil
	}
	out := []providers.Input{}
	for i := 0; i+1 < len(inputs.Content); i += 2 {
		def := deref(inputs.Content[i+1])
		in := providers.Input{Name: inputs.Content[i].Value, Type: providers.InputString}
		if t := scalar(mapValue(def, "type")); t != "" {
			in.Type = t
		}
		in.Description = scalar(mapValue(def, "description"))
		in.Required = scalar(mapValue(def, "required")) == "true"
		in.Default = scalar(mapValue(def, "default"))
		if opts := mapValue(def, "options"); opts != nil && opts.Kind == yaml.SequenceNode {
			for _, o := range opts.Content {
				in.Options = append(in.Options, scalar(o))
			}
		}
		out = append(out, in)
	}
	return out
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return deref(n.Content[i+1])
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}
