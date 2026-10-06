// Package providers defines the CI-provider abstraction used by Zea and the
// shared HTTP helpers used by concrete adapters (GitHub, GitLab).
package providers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
)

// Capabilities tell the UI which controls make sense for a provider.
type Capabilities struct {
	// MultiplePipelines is true when a repo has several independently
	// triggerable pipelines (GitHub workflows); GitLab has one per project.
	MultiplePipelines bool `json:"multiplePipelines"`
	// LiveLogs is true when job logs can be streamed while the job runs.
	LiveLogs bool `json:"liveLogs"`
	// RetryFailedJobs is true when a run can rerun only its failed jobs.
	RetryFailedJobs bool `json:"retryFailedJobs"`
	// RetryJob is true when a single job can be retried.
	RetryJob bool `json:"retryJob"`
	// PlayManualJobs is true when manual jobs can be started.
	PlayManualJobs bool `json:"playManualJobs"`
	// RetryRun is true when a whole run can be started again (all jobs).
	RetryRun bool `json:"retryRun"`
}

// CredentialField describes one credential input for the UI form.
type CredentialField struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Help      string `json:"help,omitempty"`
	Secret    bool   `json:"secret"`
	Multiline bool   `json:"multiline,omitempty"`
}

// CredentialMode is one way to authenticate (e.g. GitHub App or token).
// All fields of exactly one mode must be set.
type CredentialMode struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Help   string            `json:"help,omitempty"`
	Fields []CredentialField `json:"fields"`
}

// Info is the static description of a provider.
type Info struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	URLExample      string           `json:"urlExample"`
	APIURLHelp      string           `json:"apiURLHelp"`
	Capabilities    Capabilities     `json:"capabilities"`
	CredentialModes []CredentialMode `json:"credentialModes"`
}

// Repository is basic repository metadata, also the result of a connection test.
type Repository struct {
	FullName      string `json:"fullName"`
	DefaultBranch string `json:"defaultBranch"`
	WebURL        string `json:"webURL"`
}

// Branch is one repository branch.
type Branch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commitSHA,omitempty"`
	Protected bool   `json:"protected"`
}

// BranchList is the result of ListBranches.
type BranchList struct {
	DefaultBranch string   `json:"defaultBranch"`
	Branches      []Branch `json:"branches"`
	// Truncated is true when the repository has more branches than Zea lists.
	Truncated bool `json:"truncated"`
}

// Pipeline is something that can be triggered: a GitHub workflow or the
// GitLab project pipeline.
type Pipeline struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	Dispatchable bool   `json:"dispatchable"`
	// Reason explains why a pipeline is not dispatchable.
	Reason string `json:"reason,omitempty"`
}

// MaxBranches caps how many branches are listed per repository.
const MaxBranches = 1000

// Input types of run parameters.
const (
	InputString      = "string"
	InputBoolean     = "boolean"
	InputChoice      = "choice"
	InputNumber      = "number"
	InputEnvironment = "environment"
	// InputArray is a GitLab array input, entered as a JSON array.
	InputArray = "array"
)

// Input is one parameter declared by a pipeline definition
// (GitHub workflow_dispatch.inputs, GitLab spec:inputs).
type Input struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// RunForm describes what can be passed when starting a pipeline.
type RunForm struct {
	Inputs []Input `json:"inputs"`
	// Variables is true when arbitrary key/value variables are accepted
	// in addition to declared inputs (GitLab pipeline variables).
	Variables bool `json:"variables"`
	// PrefilledVariables are variables the pipeline definition offers in
	// its run form (GitLab variables with a description). They are passed
	// as variables, not inputs.
	PrefilledVariables []Input `json:"prefilledVariables"`
	// Warning explains why declared inputs could not be read.
	Warning string `json:"warning,omitempty"`
}

// TriggerRequest starts a pipeline at a ref.
type TriggerRequest struct {
	PipelineID string
	Ref        string
	Inputs     map[string]string
	Variables  map[string]string
}

// Status is a normalized run or job status.
type Status string

// Normalized statuses shared by all providers.
const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
	StatusManual   Status = "manual"
	StatusSkipped  Status = "skipped"
	StatusUnknown  Status = "unknown"
)

// Run is one execution of a pipeline.
type Run struct {
	ID         string    `json:"id,omitempty"`
	Number     int64     `json:"number,omitempty"`
	PipelineID string    `json:"pipelineID,omitempty"`
	Name       string    `json:"name,omitempty"`
	Title      string    `json:"title,omitempty"`
	Ref        string    `json:"ref,omitempty"`
	CommitSHA  string    `json:"commitSHA,omitempty"`
	Event      string    `json:"event,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	Status     Status    `json:"status"`
	WebURL     string    `json:"webURL,omitempty"`
	CreatedAt  time.Time `json:"createdAt,omitzero"`
	UpdatedAt  time.Time `json:"updatedAt,omitzero"`
}

// Step is one step of a job (GitHub only).
type Step struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// Job is one job of a run.
type Job struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Stage      string    `json:"stage,omitempty"`
	Status     Status    `json:"status"`
	WebURL     string    `json:"webURL,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Steps      []Step    `json:"steps,omitempty"`
}

// RunDetail is a run with its jobs.
type RunDetail struct {
	Run
	Jobs []Job `json:"jobs"`
}

// RunFilter narrows ListRuns. Empty fields match everything.
type RunFilter struct {
	PipelineID string
	Ref        string
	Limit      int
}

// MaxRuns caps how many recent runs are listed.
const MaxRuns = 50

// DefaultRuns is the number of runs listed when no limit is given.
const DefaultRuns = 20

// ClampLimit bounds a requested run count to [1, MaxRuns].
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultRuns
	case n > MaxRuns:
		return MaxRuns
	}
	return n
}

// ErrUnsupported is returned for operations a provider cannot perform.
var ErrUnsupported = errors.New("operation is not supported by this provider")

// ErrInvalidRequest marks errors caused by bad user input.
var ErrInvalidRequest = errors.New("invalid request")

// Invalidf returns an error wrapping ErrInvalidRequest.
func Invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// ValidateID checks that a provider object ID (run, workflow) is numeric.
func ValidateID(kind, id string) error {
	if id == "" || len(id) > 20 {
		return Invalidf("%s id %q is not valid", kind, id)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return Invalidf("%s id %q is not valid", kind, id)
		}
	}
	return nil
}

// Provider is implemented by every CI adapter.
type Provider interface {
	Info() Info
	// Validate checks provider-specific fields (URL shape, credentials).
	Validate(c *connections.Connection) error
	// Test verifies that the credentials can read the repository.
	Test(ctx context.Context, c *connections.Connection) (*Repository, error)
	ListBranches(ctx context.Context, c *connections.Connection) (*BranchList, error)
	// ListPipelines lists triggerable pipelines as defined at ref
	// (empty ref means the default branch).
	ListPipelines(ctx context.Context, c *connections.Connection, ref string) ([]Pipeline, error)
	// GetRunForm returns the parameters pipelineID accepts at ref.
	GetRunForm(ctx context.Context, c *connections.Connection, pipelineID, ref string) (*RunForm, error)
	// Trigger starts a pipeline. The returned Run may lack an ID when the
	// provider does not report it; the run then shows up in ListRuns.
	Trigger(ctx context.Context, c *connections.Connection, req TriggerRequest) (*Run, error)
	// ListRuns returns the most recent runs matching f, newest first.
	ListRuns(ctx context.Context, c *connections.Connection, f RunFilter) ([]Run, error)
	GetRun(ctx context.Context, c *connections.Connection, runID string) (*RunDetail, error)
	CancelRun(ctx context.Context, c *connections.Connection, runID string) error
	// RetryRun starts the run again; failedOnly reruns only failed jobs.
	RetryRun(ctx context.Context, c *connections.Connection, runID string, failedOnly bool) error
}

// CommitLinker is implemented by providers that can link to a commit page.
type CommitLinker interface {
	CommitURL(c *connections.Connection, sha string) string
}

// Registry maps provider IDs to implementations.
type Registry struct {
	byID map[string]Provider
}

// NewRegistry registers the given providers.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{byID: map[string]Provider{}}
	for _, p := range ps {
		r.byID[p.Info().ID] = p
	}
	return r
}

// Get returns the provider for id.
func (r *Registry) Get(id string) (Provider, error) {
	p, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	return p, nil
}

// Infos returns all provider descriptions sorted by ID.
func (r *Registry) Infos() []Info {
	out := make([]Info, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ValidateCredentials checks that exactly one credential mode is fully set
// and that no unknown keys are present. It returns the matched mode ID.
func ValidateCredentials(modes []CredentialMode, creds map[string]string) (string, error) {
	known := map[string]bool{}
	for _, m := range modes {
		for _, f := range m.Fields {
			known[f.Key] = true
		}
	}
	for k, v := range creds {
		if v != "" && !known[k] {
			return "", fmt.Errorf("unknown credential field %q", k)
		}
	}
	matched := ""
	for _, m := range modes {
		set := 0
		for _, f := range m.Fields {
			if creds[f.Key] != "" {
				set++
			}
		}
		switch {
		case set == len(m.Fields):
			if matched != "" {
				return "", fmt.Errorf("credentials for both %q and %q are set; keep only one", matched, m.ID)
			}
			matched = m.ID
		case set > 0:
			return "", fmt.Errorf("credential mode %q is incomplete", m.ID)
		}
	}
	if matched == "" {
		return "", fmt.Errorf("credentials are required")
	}
	return matched, nil
}
