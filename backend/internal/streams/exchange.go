package streams

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxImportBytes caps imported YAML; a manifest indents the spec.
const maxImportBytes = 2 * MaxSpecBytes

type manifestMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

type manifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   manifestMeta      `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
}

// Export renders the Stream as a declarative ConfigMap to commit to git. A
// draft is exported under the name of the Stream it was copied from, so
// syncing it replaces that Stream. The result carries no managed-by label:
// once applied, the Stream is read-only in the UI.
func Export(st *Stream, namespace string) (name, out string, err error) {
	name = st.Name
	if st.DraftOf != "" {
		name = st.DraftOf
	}
	spec, err := FormatSpec(st.Spec)
	if err != nil {
		return "", "", fmt.Errorf("encode stream: %w", err)
	}
	m := manifest{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Metadata: manifestMeta{
			Name:      ConfigMapNamePrefix + name,
			Namespace: namespace,
			Labels:    map[string]string{LabelType: TypeStream},
		},
		Data: map[string]string{KeyName: name, KeySpec: spec},
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return "", "", fmt.Errorf("encode manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", "", err
	}
	return name, b.String(), nil
}

// importDoc is the plain (non-ConfigMap) import format.
type importDoc struct {
	Name string `yaml:"name"`
	Spec `yaml:",inline"`
}

// Import reads a Stream from either an exported ConfigMap manifest or a
// plain document with name, description, params and stages.
func Import(raw string) (*Stream, error) {
	if len(raw) > maxImportBytes {
		return nil, fmt.Errorf("YAML is larger than %d KiB", maxImportBytes>>10)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("YAML is empty")
	}
	var probe struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	if probe.Kind != "" {
		return importManifest(raw, probe.Kind)
	}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	var doc importDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid stream: %v", err)
	}
	st := &Stream{Name: strings.TrimSpace(doc.Name), Spec: doc.Spec}
	if st.Name == "" {
		return nil, errors.New("name is required")
	}
	return st, nil
}

func importManifest(raw, kind string) (*Stream, error) {
	if kind != "ConfigMap" {
		return nil, fmt.Errorf("expected a ConfigMap, got %s", kind)
	}
	var m manifest
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("invalid ConfigMap: %v", err)
	}
	specRaw, ok := m.Data[KeySpec]
	if !ok {
		return nil, fmt.Errorf("the ConfigMap has no data.%q", KeySpec)
	}
	spec, err := ParseSpec(specRaw)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(m.Data[KeyName])
	if name == "" {
		name = strings.TrimPrefix(m.Metadata.Name, ConfigMapNamePrefix)
	}
	if name == "" {
		return nil, errors.New("the ConfigMap has no data.name")
	}
	return &Stream{Name: name, Spec: spec}, nil
}
