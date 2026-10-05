package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// UserAgent is sent to every CI provider.
const UserAgent = "argocd-zea"

// maxResponseBytes caps provider responses read into memory.
const maxResponseBytes = 16 << 20

// UpstreamError is a non-2xx response from a CI provider.
type UpstreamError struct {
	Provider string
	Status   int
	Message  string
}

func (e *UpstreamError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s API returned %d: %s", e.Provider, e.Status, msg)
}

// IsStatus reports whether err is an UpstreamError with the given status.
func IsStatus(err error, status int) bool {
	var ue *UpstreamError
	return errors.As(err, &ue) && ue.Status == status
}

// Request is a JSON API call.
type Request struct {
	Method  string
	URL     string
	Header  http.Header
	Body    any
	Out     any
	RawBody *[]byte
}

// Do performs req and decodes a JSON response into req.Out (or stores raw
// bytes in req.RawBody). It returns the response headers for pagination.
func Do(ctx context.Context, client *http.Client, provider string, req Request) (http.Header, error) {
	var body io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(b))
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	hr.Header.Set("User-Agent", UserAgent)
	if req.Body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("%s API request failed: %w", provider, redactURLError(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s API read failed: %w", provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Header, &UpstreamError{Provider: provider, Status: resp.StatusCode, Message: errorMessage(data)}
	}
	if req.RawBody != nil {
		*req.RawBody = data
		return resp.Header, nil
	}
	if req.Out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, req.Out); err != nil {
			return resp.Header, fmt.Errorf("%s API returned invalid JSON: %w", provider, err)
		}
	}
	return resp.Header, nil
}

// errorMessage extracts a human-readable message from GitHub/GitLab errors.
func errorMessage(data []byte) string {
	var body struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil {
		switch m := body.Message.(type) {
		case string:
			if m != "" {
				return m
			}
		case nil:
		default:
			if b, err := json.Marshal(m); err == nil {
				return string(b)
			}
		}
		if body.Error != "" {
			return body.Error
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// redactURLError drops query strings from *url.Error to avoid leaking tokens.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			u.RawQuery = ""
			return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
		}
	}
	return err
}

// RepoURL is a parsed repository web URL.
type RepoURL struct {
	Scheme string
	Host   string
	// Path is the repository path without leading slash and ".git".
	Path string
}

// ParseRepoURL parses "https://host/owner/repo(.git)".
func ParseRepoURL(raw string) (*RepoURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("url %q must be an http(s) URL", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url %q has no host", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("url must not contain credentials")
	}
	p := strings.Trim(u.Path, "/")
	p = strings.TrimSuffix(p, ".git")
	if p == "" {
		return nil, fmt.Errorf("url %q has no repository path", raw)
	}
	return &RepoURL{Scheme: u.Scheme, Host: u.Host, Path: p}, nil
}

// NormalizeAPIURL trims trailing slashes from an explicit API URL.
func NormalizeAPIURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

var commitSHARe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// CommitPageURL joins a repository web URL, a provider-specific commit path
// ("/commit/") and sha. It returns "" for anything that is not a hex SHA.
func CommitPageURL(repoURL, commitPath, sha string) string {
	u, err := ParseRepoURL(repoURL)
	if err != nil || !commitSHARe.MatchString(sha) {
		return ""
	}
	return fmt.Sprintf("%s://%s/%s%s%s", u.Scheme, u.Host, u.Path, commitPath, strings.ToLower(sha))
}
