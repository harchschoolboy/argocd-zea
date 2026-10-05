// Package github implements the Zea provider for GitHub Actions.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// ID is the provider identifier stored in Connections.
const ID = "github"

const (
	providerName = "GitHub"
	apiVersion   = "2022-11-28"
	perPage      = 100
	// maxWorkflows caps how many workflow files are inspected per request.
	maxWorkflows = 200
	// fetchConcurrency limits parallel workflow file downloads.
	fetchConcurrency = 4
)

// Provider talks to the GitHub REST API (github.com and GitHub Enterprise Server).
type Provider struct {
	client *http.Client
	now    func() time.Time
	tokens tokenCache
}

// New returns a GitHub provider using client for all API calls.
func New(client *http.Client) *Provider {
	return &Provider{client: client, now: time.Now, tokens: tokenCache{items: map[string]cachedToken{}}}
}

// Info describes the provider for the UI.
func (p *Provider) Info() providers.Info {
	return providers.Info{
		ID:         ID,
		Name:       "GitHub Actions",
		URLExample: "https://github.com/owner/repo",
		APIURLHelp: "Leave empty for github.com. For GitHub Enterprise Server use https://<host>/api/v3.",
		Capabilities: providers.Capabilities{
			MultiplePipelines: true,
			RetryFailedJobs:   true,
			RetryRun:          true,
		},
		CredentialModes: []providers.CredentialMode{
			{
				ID:    modeApp,
				Label: "GitHub App",
				Help:  "Recommended. Zea mints short-lived tokens limited to this repository and to the permissions each action needs.",
				Fields: []providers.CredentialField{
					{Key: KeyAppID, Label: "App ID or Client ID"},
					{Key: KeyInstallationID, Label: "Installation ID"},
					{Key: KeyPrivateKey, Label: "Private key (PEM)", Secret: true, Multiline: true},
				},
			},
			{
				ID:    modeToken,
				Label: "Access token",
				Help:  "Fine-grained personal access token limited to this repository (Actions: read and write, Contents: read).",
				Fields: []providers.CredentialField{
					{Key: KeyToken, Label: "Token", Secret: true},
				},
			},
		},
	}
}

var numericRe = regexp.MustCompile(`^[0-9]+$`)

// Validate checks the repository URL and credentials.
func (p *Provider) Validate(c *connections.Connection) error {
	if _, err := resolve(c); err != nil {
		return err
	}
	mode, err := providers.ValidateCredentials(p.Info().CredentialModes, c.Credentials)
	if err != nil {
		return err
	}
	if mode == modeApp {
		if !numericRe.MatchString(strings.TrimSpace(c.Credentials[KeyInstallationID])) {
			return fmt.Errorf("%s must be numeric", KeyInstallationID)
		}
		if _, err := parsePrivateKey(c.Credentials[KeyPrivateKey]); err != nil {
			return err
		}
	}
	return nil
}

// CommitURL links to a commit page of the repository.
func (p *Provider) CommitURL(c *connections.Connection, sha string) string {
	return providers.CommitPageURL(c.URL, "/commit/", sha)
}

type target struct {
	apiBase string
	owner   string
	repo    string
}

func (t *target) repoURL(format string, args ...any) string {
	return fmt.Sprintf("%s/repos/%s/%s", t.apiBase, url.PathEscape(t.owner), url.PathEscape(t.repo)) + fmt.Sprintf(format, args...)
}

func resolve(c *connections.Connection) (*target, error) {
	u, err := providers.ParseRepoURL(c.URL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("GitHub url must look like https://<host>/<owner>/<repo>, got %q", c.URL)
	}
	apiBase := providers.NormalizeAPIURL(c.APIURL)
	if apiBase == "" {
		host := strings.ToLower(u.Host)
		if host == "github.com" || host == "www.github.com" {
			apiBase = "https://api.github.com"
		} else {
			apiBase = fmt.Sprintf("%s://%s/api/v3", u.Scheme, u.Host)
		}
	}
	return &target{apiBase: apiBase, owner: parts[0], repo: parts[1]}, nil
}

func apiHeaders(bearer string) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", apiVersion)
	h.Set("Authorization", "Bearer "+bearer)
	return h
}

func (p *Provider) get(ctx context.Context, c *connections.Connection, t *target, rawURL string, out any) (http.Header, error) {
	tok, err := p.token(ctx, c, t, readPermissions)
	if err != nil {
		return nil, err
	}
	return providers.Do(ctx, p.client, providerName, providers.Request{URL: rawURL, Header: apiHeaders(tok), Out: out})
}

type repoJSON struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// Test reads the repository with the configured credentials.
func (p *Provider) Test(ctx context.Context, c *connections.Connection) (*providers.Repository, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	return p.repository(ctx, c, t)
}

func (p *Provider) repository(ctx context.Context, c *connections.Connection, t *target) (*providers.Repository, error) {
	var r repoJSON
	if _, err := p.get(ctx, c, t, t.repoURL(""), &r); err != nil {
		return nil, err
	}
	return &providers.Repository{FullName: r.FullName, DefaultBranch: r.DefaultBranch, WebURL: r.HTMLURL}, nil
}

// ListBranches lists up to providers.MaxBranches branches.
func (p *Provider) ListBranches(ctx context.Context, c *connections.Connection) (*providers.BranchList, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	repo, err := p.repository(ctx, c, t)
	if err != nil {
		return nil, err
	}
	out := &providers.BranchList{DefaultBranch: repo.DefaultBranch, Branches: []providers.Branch{}}
	next := t.repoURL("/branches?per_page=%d", perPage)
	for next != "" {
		if len(out.Branches) >= providers.MaxBranches {
			out.Truncated = true
			break
		}
		var page []struct {
			Name      string `json:"name"`
			Protected bool   `json:"protected"`
			Commit    struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		h, err := p.get(ctx, c, t, next, &page)
		if err != nil {
			return nil, err
		}
		for _, b := range page {
			out.Branches = append(out.Branches, providers.Branch{Name: b.Name, CommitSHA: b.Commit.SHA, Protected: b.Protected})
		}
		next = nextLink(t.apiBase, h)
	}
	return out, nil
}

type workflowJSON struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

// ListPipelines lists workflows and marks those that can be started manually
// (workflow_dispatch) at ref.
func (p *Provider) ListPipelines(ctx context.Context, c *connections.Connection, ref string) ([]providers.Pipeline, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		repo, err := p.repository(ctx, c, t)
		if err != nil {
			return nil, err
		}
		ref = repo.DefaultBranch
	}

	var workflows []workflowJSON
	next := t.repoURL("/actions/workflows?per_page=%d", perPage)
	for next != "" && len(workflows) < maxWorkflows {
		var page struct {
			Workflows []workflowJSON `json:"workflows"`
		}
		h, err := p.get(ctx, c, t, next, &page)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, page.Workflows...)
		next = nextLink(t.apiBase, h)
	}

	out := make([]providers.Pipeline, 0, len(workflows))
	for _, w := range workflows {
		// Dynamic workflows (Pages, Dependabot, CodeQL default setup) live
		// outside .github/workflows and cannot be dispatched.
		if !strings.HasPrefix(w.Path, ".github/workflows/") {
			continue
		}
		out = append(out, providers.Pipeline{ID: strconv.FormatInt(w.ID, 10), Name: w.Name, Path: w.Path})
	}

	sem := make(chan struct{}, fetchConcurrency)
	var wg sync.WaitGroup
	for i := range out {
		state := workflowState(workflows, out[i].ID)
		if state != "active" {
			out[i].Reason = fmt.Sprintf("workflow is %s", state)
			continue
		}
		wg.Add(1)
		go func(pl *providers.Pipeline) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pl.Dispatchable, pl.Reason = p.inspectWorkflow(ctx, c, t, pl.Path, ref)
		}(&out[i])
	}
	wg.Wait()
	return out, nil
}

func workflowState(ws []workflowJSON, id string) string {
	for _, w := range ws {
		if strconv.FormatInt(w.ID, 10) == id {
			return w.State
		}
	}
	return "unknown"
}

func (p *Provider) inspectWorkflow(ctx context.Context, c *connections.Connection, t *target, path, ref string) (bool, string) {
	raw, err := p.workflowFile(ctx, c, t, path, ref)
	if providers.IsStatus(err, http.StatusNotFound) {
		return false, fmt.Sprintf("workflow file does not exist on %s", ref)
	}
	if err != nil {
		return false, err.Error()
	}
	ok, err := hasWorkflowDispatch(raw)
	if err != nil {
		return false, fmt.Sprintf("cannot parse workflow file: %v", err)
	}
	if !ok {
		return false, fmt.Sprintf("workflow has no workflow_dispatch trigger on %s", ref)
	}
	return true, ""
}

// workflowFile downloads a raw file from the repository at ref.
func (p *Provider) workflowFile(ctx context.Context, c *connections.Connection, t *target, path, ref string) ([]byte, error) {
	tok, err := p.token(ctx, c, t, readPermissions)
	if err != nil {
		return nil, err
	}
	h := apiHeaders(tok)
	h.Set("Accept", "application/vnd.github.raw+json")
	var raw []byte
	_, err = providers.Do(ctx, p.client, providerName, providers.Request{
		URL:     t.repoURL("/contents/%s?ref=%s", escapePath(path), url.QueryEscape(ref)),
		Header:  h,
		RawBody: &raw,
	})
	return raw, err
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextLink returns the rel="next" URL from a Link header. Links to another
// origin are ignored so credentials are never sent outside the API host.
func nextLink(apiBase string, h http.Header) string {
	base, err := url.Parse(apiBase)
	if err != nil {
		return ""
	}
	for _, v := range h.Values("Link") {
		if m := linkNextRe.FindStringSubmatch(v); m != nil {
			u, err := url.Parse(m[1])
			if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
				return ""
			}
			return m[1]
		}
	}
	return ""
}
