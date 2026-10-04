// Package providers defines the CI-provider abstraction used by Zea and the
// shared HTTP helpers used by concrete adapters (GitHub, GitLab).
package providers

import (
	"context"
	"fmt"
	"sort"

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
