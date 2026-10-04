// Package gitlab implements the Zea provider for GitLab CI/CD.
package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// ID is the provider identifier stored in Connections.
const ID = "gitlab"

// KeyToken is the credential key for the access token.
const KeyToken = "token"

const (
	providerName      = "GitLab"
	perPage           = 100
	defaultCIPath     = ".gitlab-ci.yml"
	projectPipelineID = "default"
)

// Provider talks to the GitLab REST API v4 (gitlab.com and self-managed).
type Provider struct {
	client *http.Client
}

// New returns a GitLab provider using client for all API calls.
func New(client *http.Client) *Provider {
	return &Provider{client: client}
}

// Info describes the provider for the UI.
func (p *Provider) Info() providers.Info {
	return providers.Info{
		ID:         ID,
		Name:       "GitLab CI/CD",
		URLExample: "https://gitlab.com/group/project",
		APIURLHelp: "Leave empty to use <host>/api/v4. Set it when GitLab is served under a sub-path, e.g. https://host/gitlab/api/v4.",
		Capabilities: providers.Capabilities{
			LiveLogs:        true,
			RetryFailedJobs: true,
			RetryJob:        true,
			PlayManualJobs:  true,
		},
		CredentialModes: []providers.CredentialMode{
			{
				ID:    "token",
				Label: "Access token",
				Help:  "Project access token with the Developer role and the api scope.",
				Fields: []providers.CredentialField{
					{Key: KeyToken, Label: "Token", Secret: true},
				},
			},
		},
	}
}

// Validate checks the repository URL and credentials.
func (p *Provider) Validate(c *connections.Connection) error {
	if _, err := resolve(c); err != nil {
		return err
	}
	_, err := providers.ValidateCredentials(p.Info().CredentialModes, c.Credentials)
	return err
}

type target struct {
	apiBase string
	// project is the URL-encoded "group/subgroup/project" path.
	project string
}

func (t *target) projectURL(format string, args ...any) string {
	return fmt.Sprintf("%s/projects/%s", t.apiBase, t.project) + fmt.Sprintf(format, args...)
}

func resolve(c *connections.Connection) (*target, error) {
	u, err := providers.ParseRepoURL(c.URL)
	if err != nil {
		return nil, err
	}
	path := u.Path
	apiBase := providers.NormalizeAPIURL(c.APIURL)
	if apiBase == "" {
		apiBase = fmt.Sprintf("%s://%s/api/v4", u.Scheme, u.Host)
	} else if au, err := url.Parse(apiBase); err == nil && au.Host == u.Host {
		// GitLab under a sub-path: drop that prefix from the project path.
		prefix := strings.Trim(strings.TrimSuffix(au.Path, "/api/v4"), "/")
		if prefix != "" {
			path = strings.TrimPrefix(strings.TrimPrefix(path, prefix), "/")
		}
	}
	if !strings.Contains(path, "/") {
		return nil, fmt.Errorf("GitLab url must look like https://<host>/<group>/<project>, got %q", c.URL)
	}
	return &target{apiBase: apiBase, project: url.PathEscape(path)}, nil
}

func (p *Provider) do(ctx context.Context, c *connections.Connection, req providers.Request) (http.Header, error) {
	h := http.Header{}
	h.Set("PRIVATE-TOKEN", c.Credentials[KeyToken])
	h.Set("Accept", "application/json")
	req.Header = h
	return providers.Do(ctx, p.client, providerName, req)
}

type projectJSON struct {
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
	CIConfigPath      string `json:"ci_config_path"`
}

func (p *Provider) project(ctx context.Context, c *connections.Connection, t *target) (*projectJSON, error) {
	var pj projectJSON
	if _, err := p.do(ctx, c, providers.Request{URL: t.projectURL(""), Out: &pj}); err != nil {
		return nil, err
	}
	return &pj, nil
}

// Test reads the project with the configured token.
func (p *Provider) Test(ctx context.Context, c *connections.Connection) (*providers.Repository, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	pj, err := p.project(ctx, c, t)
	if err != nil {
		return nil, err
	}
	return &providers.Repository{FullName: pj.PathWithNamespace, DefaultBranch: pj.DefaultBranch, WebURL: pj.WebURL}, nil
}

// ListBranches lists up to providers.MaxBranches branches.
func (p *Provider) ListBranches(ctx context.Context, c *connections.Connection) (*providers.BranchList, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	pj, err := p.project(ctx, c, t)
	if err != nil {
		return nil, err
	}
	out := &providers.BranchList{DefaultBranch: pj.DefaultBranch, Branches: []providers.Branch{}}
	page := 1
	for page > 0 {
		if len(out.Branches) >= providers.MaxBranches {
			out.Truncated = true
			break
		}
		var items []struct {
			Name      string `json:"name"`
			Protected bool   `json:"protected"`
			Commit    struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		h, err := p.do(ctx, c, providers.Request{URL: t.projectURL("/repository/branches?per_page=%d&page=%d", perPage, page), Out: &items})
		if err != nil {
			return nil, err
		}
		for _, b := range items {
			out.Branches = append(out.Branches, providers.Branch{Name: b.Name, CommitSHA: b.Commit.ID, Protected: b.Protected})
		}
		page, _ = strconv.Atoi(h.Get("X-Next-Page"))
	}
	return out, nil
}

// ListPipelines returns the single project pipeline. It is marked not
// dispatchable when the CI configuration file is missing at ref.
func (p *Provider) ListPipelines(ctx context.Context, c *connections.Connection, ref string) ([]providers.Pipeline, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	pj, err := p.project(ctx, c, t)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = pj.DefaultBranch
	}
	ciPath := pj.CIConfigPath
	if ciPath == "" {
		ciPath = defaultCIPath
	}
	pl := providers.Pipeline{ID: projectPipelineID, Name: "Pipeline", Path: ciPath, Dispatchable: true}

	// "path@group/project" and URLs point outside the repository.
	if strings.Contains(ciPath, "@") || strings.Contains(ciPath, "://") {
		return []providers.Pipeline{pl}, nil
	}
	_, err = p.do(ctx, c, providers.Request{
		Method: http.MethodHead,
		URL:    t.projectURL("/repository/files/%s?ref=%s", url.PathEscape(ciPath), url.QueryEscape(ref)),
	})
	switch {
	case providers.IsStatus(err, http.StatusNotFound):
		pl.Dispatchable = false
		pl.Reason = fmt.Sprintf("%s does not exist on %s", ciPath, ref)
	case err != nil:
		return nil, err
	}
	return []providers.Pipeline{pl}, nil
}
