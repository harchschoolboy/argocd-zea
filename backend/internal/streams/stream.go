// Package streams stores Zea Streams: ordered, parameterised runs of
// pipelines from several Connections. A Stream is a list of stages; the
// steps of a stage run in parallel and a stage waits for the previous one
// unless a step names its dependencies explicitly with needs.
package streams

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Limits keep Streams small enough for a ConfigMap and readable in the UI.
const (
	MaxStages        = 20
	MaxStepsPerStage = 20
	MaxSteps         = 50
	MaxParams        = 30
	MaxStepValues    = 50
	MaxOptions       = 50
	// MaxSpecBytes caps the stored YAML; a ConfigMap holds at most 1 MiB.
	MaxSpecBytes       = 256 << 10
	maxNameLen         = 50
	maxDescriptionLen  = 1000
	maxValueLen        = 4096
	maxKeyLen          = 255
	maxStageNameLen    = 60
	maxStepNameLen     = 80
	maxParamDefaultLen = 1024
	minTimeout         = time.Minute
	maxTimeout         = 72 * time.Hour
	maxRetryDelay      = time.Hour
)

// Step retry limits.
const (
	MaxStepRetries    = 5
	DefaultRetryDelay = 30 * time.Second
)

// Parameter types.
const (
	ParamString  = "string"
	ParamChoice  = "choice"
	ParamBoolean = "boolean"
	// ParamBranch is a branch of Param.Connection, picked from a list.
	ParamBranch = "branch"
)

// Step conditions relative to the step's dependencies.
const (
	// WhenSuccess runs the step when all dependencies succeeded (default).
	WhenSuccess = "success"
	// WhenFailure runs the step when at least one dependency failed.
	WhenFailure = "failure"
	// WhenAlways runs the step once all dependencies finished.
	WhenAlways = "always"
)

var (
	// ErrNotFound is returned when a Stream does not exist.
	ErrNotFound = errors.New("stream not found")
	// ErrAlreadyExists is returned when creating a duplicate Stream.
	ErrAlreadyExists = errors.New("stream already exists")
	// ErrReadOnly is returned when modifying a declaratively managed Stream.
	ErrReadOnly = errors.New("stream is managed declaratively and is read-only in Zea; edit a draft and export it")
	// ErrConflict is returned when the Stream changed since it was read.
	ErrConflict = errors.New("stream was changed by someone else; reload it and apply your changes again")
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	stepIDRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	paramRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,49}$`)
	variableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)
)

// Spec is the editable content of a Stream, stored as YAML.
type Spec struct {
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Params      []Param `json:"params" yaml:"params,omitempty"`
	Stages      []Stage `json:"stages" yaml:"stages"`
}

// Param is a value asked when the Stream starts, referenced in steps as
// ${{ params.<name> }}.
type Param struct {
	Name        string   `json:"name" yaml:"name"`
	Type        string   `json:"type" yaml:"type"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Default     string   `json:"default,omitempty" yaml:"default,omitempty"`
	Required    bool     `json:"required,omitempty" yaml:"required,omitempty"`
	Options     []string `json:"options,omitempty" yaml:"options,omitempty"`
	// Connection lists the branches of a branch parameter.
	Connection string `json:"connection,omitempty" yaml:"connection,omitempty"`
}

// Stage groups steps that run in parallel.
type Stage struct {
	Name  string `json:"name,omitempty" yaml:"name,omitempty"`
	Steps []Step `json:"steps" yaml:"steps"`
}

// Step runs one pipeline of a Connection. Ref, Inputs and Variables may
// contain ${{ ... }} references.
type Step struct {
	ID         string            `json:"id" yaml:"id"`
	Name       string            `json:"name,omitempty" yaml:"name,omitempty"`
	Connection string            `json:"connection" yaml:"connection"`
	Pipeline   string            `json:"pipeline" yaml:"pipeline"`
	Ref        string            `json:"ref" yaml:"ref"`
	Inputs     map[string]string `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Variables  map[string]string `json:"variables,omitempty" yaml:"variables,omitempty"`
	// Needs lists the steps this one waits for. Empty means every step of
	// the previous stage. Only steps of earlier stages may be named.
	Needs []string `json:"needs,omitempty" yaml:"needs,omitempty"`
	When  string   `json:"when,omitempty" yaml:"when,omitempty"`
	// Timeout is a Go duration ("90m"); empty uses the engine default.
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	// ContinueOnError lets dependants treat a failure of this step as success.
	ContinueOnError bool `json:"continueOnError,omitempty" yaml:"continueOnError,omitempty"`
	// Retries is how often a failed pipeline is started again before the
	// step fails; 0 leaves retrying to the user.
	Retries int `json:"retries,omitempty" yaml:"retries,omitempty"`
	// RetryDelay is a Go duration to wait before a retry; empty uses
	// DefaultRetryDelay.
	RetryDelay string `json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`
}

// Stream is a named Spec with storage metadata.
type Stream struct {
	Name string
	// DraftOf names the Stream this draft was copied from. Exported drafts
	// replace that Stream.
	DraftOf string
	Spec
	// Editable is false for Streams managed declaratively (git).
	Editable bool
	// ConfigMapName is the backing ConfigMap's name.
	ConfigMapName string
	// ResourceVersion guards updates against concurrent edits.
	ResourceVersion string
	// ParseError is set when the stored spec cannot be read.
	ParseError string
}

// Problem is a reason why a Stream cannot run. Step or Param point to the
// element the problem belongs to, when there is one.
type Problem struct {
	Step    string `json:"step,omitempty"`
	Param   string `json:"param,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	switch {
	case p.Step != "":
		return fmt.Sprintf("step %q: %s", p.Step, p.Message)
	case p.Param != "":
		return fmt.Sprintf("param %q: %s", p.Param, p.Message)
	}
	return p.Message
}

// ValidateName checks a Stream name.
func ValidateName(name string) error {
	if len(name) > maxNameLen || !nameRe.MatchString(name) {
		return fmt.Errorf("invalid stream name %q: use lowercase letters, digits and '-', max %d characters", name, maxNameLen)
	}
	return nil
}

// Validate rejects Streams that cannot be stored: bad names and anything
// over the limits. Incomplete Streams are accepted so that work in progress
// can be saved; Problems reports what keeps them from running.
func (s *Stream) Validate() error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	if s.DraftOf != "" {
		if err := ValidateName(s.DraftOf); err != nil {
			return fmt.Errorf("draftOf: %w", err)
		}
	}
	if len(s.Description) > maxDescriptionLen {
		return fmt.Errorf("description is longer than %d characters", maxDescriptionLen)
	}
	if len(s.Params) > MaxParams {
		return fmt.Errorf("at most %d params are allowed", MaxParams)
	}
	for _, p := range s.Params {
		if len(p.Name) > maxKeyLen || len(p.Connection) > maxKeyLen || len(p.Type) > maxKeyLen {
			return errors.New("param name, type or connection is too long")
		}
		if len(p.Options) > MaxOptions {
			return fmt.Errorf("param %q: at most %d options are allowed", p.Name, MaxOptions)
		}
		if len(p.Default) > maxParamDefaultLen || len(p.Description) > maxDescriptionLen {
			return fmt.Errorf("param %q: default or description is too long", p.Name)
		}
		for _, o := range p.Options {
			if len(o) > maxParamDefaultLen {
				return fmt.Errorf("param %q: option is too long", p.Name)
			}
		}
	}
	if len(s.Stages) > MaxStages {
		return fmt.Errorf("at most %d stages are allowed", MaxStages)
	}
	total := 0
	for i, st := range s.Stages {
		if len(st.Name) > maxStageNameLen {
			return fmt.Errorf("stage %d: name is longer than %d characters", i+1, maxStageNameLen)
		}
		if len(st.Steps) > MaxStepsPerStage {
			return fmt.Errorf("stage %d: at most %d steps are allowed", i+1, MaxStepsPerStage)
		}
		total += len(st.Steps)
		for _, step := range st.Steps {
			if err := validateStepSize(step); err != nil {
				return err
			}
		}
	}
	if total > MaxSteps {
		return fmt.Errorf("at most %d steps are allowed", MaxSteps)
	}
	spec, err := FormatSpec(s.Spec)
	if err != nil {
		return fmt.Errorf("encode stream: %w", err)
	}
	if len(spec) > MaxSpecBytes {
		return fmt.Errorf("the stream is larger than %d KiB", MaxSpecBytes>>10)
	}
	return nil
}

func validateStepSize(step Step) error {
	if len(step.ID) > maxKeyLen || len(step.Connection) > maxKeyLen || len(step.Pipeline) > maxKeyLen ||
		len(step.When) > maxKeyLen || len(step.Timeout) > maxKeyLen || len(step.RetryDelay) > maxKeyLen {
		return errors.New("step id, connection, pipeline, when, timeout or retry delay is too long")
	}
	if len(step.Name) > maxStepNameLen {
		return fmt.Errorf("step %q: name is longer than %d characters", step.ID, maxStepNameLen)
	}
	if len(step.Ref) > maxValueLen {
		return fmt.Errorf("step %q: ref is too long", step.ID)
	}
	if len(step.Needs) > MaxSteps {
		return fmt.Errorf("step %q: too many needs", step.ID)
	}
	if len(step.Inputs)+len(step.Variables) > MaxStepValues {
		return fmt.Errorf("step %q: at most %d inputs and variables are allowed", step.ID, MaxStepValues)
	}
	for _, m := range []map[string]string{step.Inputs, step.Variables} {
		for k, v := range m {
			if len(k) > maxKeyLen || len(v) > maxValueLen {
				return fmt.Errorf("step %q: %q or its value is too long", step.ID, k)
			}
		}
	}
	return nil
}

// Problems lists everything that keeps the Stream from running, without
// looking at Connections (the server checks those).
func (s *Stream) Problems() []Problem {
	if s.ParseError != "" {
		return []Problem{{Message: s.ParseError}}
	}
	var out []Problem
	add := func(p Problem) { out = append(out, p) }

	params := map[string]bool{}
	for i, p := range s.Params {
		if !paramRe.MatchString(p.Name) {
			add(Problem{Param: p.Name, Message: fmt.Sprintf("param %d: invalid name, use letters, digits and '_'", i+1)})
			continue
		}
		if params[p.Name] {
			add(Problem{Param: p.Name, Message: "duplicate param name"})
			continue
		}
		params[p.Name] = true
		for _, msg := range paramProblems(p) {
			add(Problem{Param: p.Name, Message: msg})
		}
	}

	if len(s.Stages) == 0 {
		add(Problem{Message: "the stream has no stages"})
	}
	g := s.Graph()
	ids := map[string]bool{}
	earlier := map[string]bool{}
	for i, st := range s.Stages {
		if len(st.Steps) == 0 {
			add(Problem{Message: fmt.Sprintf("stage %d has no steps", i+1)})
		}
		for _, step := range st.Steps {
			if !stepIDRe.MatchString(step.ID) {
				add(Problem{Step: step.ID, Message: "invalid id: start with a letter, use lowercase letters, digits, '-' and '_', max 40 characters"})
			} else if ids[step.ID] {
				add(Problem{Step: step.ID, Message: "duplicate step id"})
			}
			ids[step.ID] = true
			for _, msg := range stepProblems(step, earlier, g.Upstream(step.ID), params) {
				add(Problem{Step: step.ID, Message: msg})
			}
		}
		for _, step := range st.Steps {
			earlier[step.ID] = true
		}
	}
	return out
}

func paramProblems(p Param) []string {
	var out []string
	switch p.Type {
	case ParamString:
	case ParamChoice:
		if len(p.Options) == 0 {
			out = append(out, "a choice needs options")
		}
		if p.Default != "" && !contains(p.Options, p.Default) {
			out = append(out, "default is not one of the options")
		}
	case ParamBoolean:
		if p.Default != "" && p.Default != "true" && p.Default != "false" {
			out = append(out, `boolean default must be "true" or "false"`)
		}
	case ParamBranch:
		if p.Connection == "" {
			out = append(out, "a branch param needs a connection to list branches from")
		}
	case "":
		out = append(out, "type is required")
	default:
		out = append(out, fmt.Sprintf("unknown type %q", p.Type))
	}
	if p.Type != ParamChoice && len(p.Options) > 0 {
		out = append(out, "only choice params have options")
	}
	if p.Type != ParamBranch && p.Connection != "" {
		out = append(out, "only branch params have a connection")
	}
	return out
}

// stepProblems checks one step. earlier holds the ids of earlier stages,
// upstream the steps it waits for, directly or not.
func stepProblems(step Step, earlier, upstream, params map[string]bool) []string {
	var out []string
	if step.Connection == "" {
		out = append(out, "connection is required")
	}
	if step.Pipeline == "" {
		out = append(out, "pipeline is required")
	}
	if strings.TrimSpace(step.Ref) == "" {
		out = append(out, "ref (branch or tag) is required")
	}
	for _, n := range step.Needs {
		switch {
		case n == step.ID:
			out = append(out, "a step cannot need itself")
		case !earlier[n]:
			out = append(out, fmt.Sprintf("needs %q, which is not a step of an earlier stage", n))
		}
	}
	switch step.When {
	case "", WhenSuccess, WhenFailure, WhenAlways:
	default:
		out = append(out, fmt.Sprintf("unknown when %q: use success, failure or always", step.When))
	}
	if step.Timeout != "" {
		d, err := time.ParseDuration(step.Timeout)
		if err != nil || d < minTimeout || d > maxTimeout {
			out = append(out, fmt.Sprintf("timeout %q must be a duration between %s and %s, e.g. 90m", step.Timeout, minTimeout, maxTimeout))
		}
	}
	if step.Retries < 0 || step.Retries > MaxStepRetries {
		out = append(out, fmt.Sprintf("retries must be between 0 and %d", MaxStepRetries))
	}
	if step.RetryDelay != "" {
		if d, err := time.ParseDuration(step.RetryDelay); err != nil || d < 0 || d > maxRetryDelay {
			out = append(out, fmt.Sprintf("retry delay %q must be a duration up to %s, e.g. 30s or 5m", step.RetryDelay, maxRetryDelay))
		}
	}
	for _, k := range sortedKeys(step.Variables) {
		if !variableRe.MatchString(k) {
			out = append(out, fmt.Sprintf("invalid variable name %q", k))
		}
	}
	for k := range step.Inputs {
		if strings.TrimSpace(k) == "" {
			out = append(out, "input names cannot be empty")
			break
		}
	}
	check := func(field, v string) {
		for _, msg := range checkTemplate(v, params, upstream) {
			out = append(out, field+": "+msg)
		}
	}
	check("ref", step.Ref)
	for _, k := range sortedKeys(step.Inputs) {
		check("input "+k, step.Inputs[k])
	}
	for _, k := range sortedKeys(step.Variables) {
		check("variable "+k, step.Variables[k])
	}
	return out
}

// Connections returns the Connections the Stream uses, without duplicates.
func (s *Stream) Connections() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, p := range s.Params {
		if p.Type == ParamBranch {
			add(p.Connection)
		}
	}
	for _, st := range s.Stages {
		for _, step := range st.Steps {
			add(step.Connection)
		}
	}
	return out
}

// Graph maps step ids to the ids they wait for directly.
type Graph map[string][]string

// Graph resolves stages and needs into direct dependencies. A step without
// needs waits for every step of the closest earlier non-empty stage.
func (s *Stream) Graph() Graph {
	g := Graph{}
	var prev []string
	for _, st := range s.Stages {
		var cur []string
		for _, step := range st.Steps {
			deps := step.Needs
			if len(deps) == 0 {
				deps = prev
			}
			g[step.ID] = append([]string{}, deps...)
			cur = append(cur, step.ID)
		}
		if len(cur) > 0 {
			prev = cur
		}
	}
	return g
}

// Upstream returns every step id waits for, directly or not.
func (g Graph) Upstream(id string) map[string]bool {
	out := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, d := range g[n] {
			if d != id && !out[d] {
				out[d] = true
				walk(d)
			}
		}
	}
	walk(id)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
