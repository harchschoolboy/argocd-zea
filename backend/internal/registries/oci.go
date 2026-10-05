package registries

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

const ociName = "Registry"

const (
	mediaOCIIndex      = "application/vnd.oci.image.index.v1+json"
	mediaOCIManifest   = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerList    = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaDockerV2      = "application/vnd.docker.distribution.manifest.v2+json"
	maxRegistryBody    = 16 << 20
	describeTTL        = 5 * time.Minute
	describeWorkers    = 6
	maxCacheEntries    = 5000
	tokenExpiryMargin  = 10 * time.Second
	defaultTokenExpiry = 60 * time.Second
)

var manifestAccept = []string{mediaOCIIndex, mediaOCIManifest, mediaDockerList, mediaDockerV2}

// repoPathRe follows the distribution spec repository name grammar loosely.
var repoPathRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

// OCI talks to any registry implementing the OCI distribution API with the
// catalog extension (Harbor, Zot, distribution/registry, Nexus, ...).
type OCI struct {
	client *http.Client

	mu        sync.Mutex
	tokens    map[string]cachedToken
	described map[string]describedTag
	created   map[string]time.Time
}

type cachedToken struct {
	value   string
	expires time.Time
}

type describedTag struct {
	tag     Tag
	expires time.Time
}

// NewOCI returns a generic OCI registry client.
func NewOCI(client *http.Client) *OCI {
	return &OCI{
		client:    client,
		tokens:    map[string]cachedToken{},
		described: map[string]describedTag{},
		created:   map[string]time.Time{},
	}
}

func (o *OCI) Info() KindInfo {
	return KindInfo{
		ID:         "oci",
		Name:       "OCI registry (generic)",
		URLExample: "registry.example.com/team",
		URLHelp:    "Registry host with an optional namespace; only repositories below it are listed. The registry must support the catalog API (/v2/_catalog).",
		CredentialModes: []providers.CredentialMode{
			{ID: "anonymous", Label: "Anonymous"},
			{
				ID:    "basic",
				Label: "Username and password",
				Fields: []providers.CredentialField{
					{Key: CredUsername, Label: "Username", Secret: false},
					{Key: CredPassword, Label: "Password or token", Secret: true},
				},
			},
			{
				ID:    "dockerconfig",
				Label: "Docker config",
				Help:  "A .dockerconfigjson with an entry for the registry host.",
				Fields: []providers.CredentialField{
					{Key: CredDockerConfig, Label: ".dockerconfigjson", Secret: true, Multiline: true},
				},
			},
		},
	}
}

func (o *OCI) Validate(r *Registry) error {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return err
	}
	if loc.Namespace != "" && !repoPathRe.MatchString(loc.Namespace) {
		return fmt.Errorf("registry namespace %q is not a valid repository path", loc.Namespace)
	}
	if r.Credentials[CredDockerConfig] != "" {
		if _, _, err := DockerConfigAuth(r.Credentials[CredDockerConfig], loc.Host); err != nil {
			return err
		}
	}
	return nil
}

func (o *OCI) ListRepositories(ctx context.Context, r *Registry) ([]Repository, error) {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if loc.Namespace != "" {
		prefix = loc.Namespace + "/"
	}
	out := []Repository{}
	next := loc.Base() + "/v2/_catalog?n=1000"
	for page := 0; next != "" && page < maxPages; page++ {
		h, body, err := o.fetch(ctx, r, loc, next, "registry:catalog:*", []string{"application/json"})
		if err != nil {
			if providers.IsStatus(err, http.StatusNotFound) {
				return nil, fmt.Errorf("registry does not support the catalog API (/v2/_catalog): %w", err)
			}
			return nil, err
		}
		var c struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &c); err != nil {
			return nil, fmt.Errorf("registry catalog is not valid JSON: %v", err)
		}
		for _, name := range c.Repositories {
			if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
				out = append(out, Repository{Name: name[len(prefix):]})
			}
		}
		next = nextLink(loc, h)
	}
	return out, nil
}

func (o *OCI) ListTags(ctx context.Context, r *Registry, repo string) ([]Tag, error) {
	loc, path, err := o.repoPath(r, repo)
	if err != nil {
		return nil, err
	}
	out := []Tag{}
	next := loc.Base() + "/v2/" + path + "/tags/list?n=1000"
	for page := 0; next != "" && page < maxPages; page++ {
		h, body, err := o.fetch(ctx, r, loc, next, pullScope(path), []string{"application/json"})
		if err != nil {
			return nil, err
		}
		var tl struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &tl); err != nil {
			return nil, fmt.Errorf("registry tag list is not valid JSON: %v", err)
		}
		for _, t := range tl.Tags {
			out = append(out, Tag{Name: t})
		}
		next = nextLink(loc, h)
	}
	return out, nil
}

// DescribeTags reads each tag's manifest and image config to learn digest,
// size and creation time. Failures leave the tag as it was.
func (o *OCI) DescribeTags(ctx context.Context, r *Registry, repo string, tags []Tag) []Tag {
	out := append([]Tag(nil), tags...)
	loc, path, err := o.repoPath(r, repo)
	if err != nil {
		return out
	}
	sem := make(chan struct{}, describeWorkers)
	var wg sync.WaitGroup
	for i := range out {
		if !out[i].PushedAt.IsZero() {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if t, ok := o.describe(ctx, r, loc, path, out[i].Name); ok {
				out[i] = t
			}
		}(i)
	}
	wg.Wait()
	return out
}

type manifestJSON struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform *struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

func (o *OCI) describe(ctx context.Context, r *Registry, loc *Location, path, tag string) (Tag, bool) {
	key := r.Name + "|" + loc.Host + "|" + path + "|" + tag
	o.mu.Lock()
	if d, ok := o.described[key]; ok && time.Now().Before(d.expires) {
		o.mu.Unlock()
		return d.tag, true
	}
	o.mu.Unlock()

	scope := pullScope(path)
	h, body, err := o.fetch(ctx, r, loc, loc.Base()+"/v2/"+path+"/manifests/"+url.PathEscape(tag), scope, manifestAccept)
	if err != nil {
		return Tag{}, false
	}
	digest := h.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	var m manifestJSON
	if json.Unmarshal(body, &m) != nil {
		return Tag{}, false
	}
	if len(m.Manifests) > 0 {
		child := pickPlatform(m)
		if child == "" {
			return Tag{}, false
		}
		_, body, err = o.fetch(ctx, r, loc, loc.Base()+"/v2/"+path+"/manifests/"+child, scope, manifestAccept)
		if err != nil || json.Unmarshal(body, &m) != nil {
			return Tag{}, false
		}
	}
	t := Tag{Name: tag, Digest: digest, SizeBytes: m.Config.Size}
	for _, l := range m.Layers {
		t.SizeBytes += l.Size
	}
	if m.Config.Digest != "" {
		t.PushedAt = o.configCreated(ctx, r, loc, path, m.Config.Digest)
	}
	o.mu.Lock()
	if len(o.described) >= maxCacheEntries {
		o.described = map[string]describedTag{}
	}
	o.described[key] = describedTag{tag: t, expires: time.Now().Add(describeTTL)}
	o.mu.Unlock()
	return t, true
}

// pickPlatform chooses linux/amd64 from an index, else the first real image.
func pickPlatform(m manifestJSON) string {
	first := ""
	for _, c := range m.Manifests {
		if c.Platform == nil || c.Platform.OS == "unknown" {
			continue
		}
		if c.Platform.OS == "linux" && c.Platform.Architecture == "amd64" {
			return c.Digest
		}
		if first == "" {
			first = c.Digest
		}
	}
	if first == "" && len(m.Manifests) > 0 {
		first = m.Manifests[0].Digest
	}
	return first
}

// configCreated returns the "created" time of an image config blob. Configs
// are content-addressed, so results are cached by digest.
func (o *OCI) configCreated(ctx context.Context, r *Registry, loc *Location, path, digest string) time.Time {
	o.mu.Lock()
	if t, ok := o.created[digest]; ok {
		o.mu.Unlock()
		return t
	}
	o.mu.Unlock()
	_, body, err := o.fetch(ctx, r, loc, loc.Base()+"/v2/"+path+"/blobs/"+url.PathEscape(digest), pullScope(path), nil)
	if err != nil {
		return time.Time{}
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if json.Unmarshal(body, &cfg) != nil {
		return time.Time{}
	}
	o.mu.Lock()
	if len(o.created) >= maxCacheEntries {
		o.created = map[string]time.Time{}
	}
	o.created[digest] = cfg.Created
	o.mu.Unlock()
	return cfg.Created
}

func (o *OCI) repoPath(r *Registry, repo string) (*Location, string, error) {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return nil, "", err
	}
	path := loc.Path(repo)
	if !repoPathRe.MatchString(path) {
		return nil, "", providers.Invalidf("repository %q is not valid", repo)
	}
	return loc, path, nil
}

func pullScope(path string) string {
	return "repository:" + path + ":pull"
}

// fetch performs a GET, answering a 401 challenge once (Bearer token or
// Basic auth, as docker does). Bearer tokens are cached per scope.
func (o *OCI) fetch(ctx context.Context, r *Registry, loc *Location, rawURL, scope string, accept []string) (http.Header, []byte, error) {
	key := o.tokenKey(r, loc, scope)
	auth := o.cachedToken(key)
	h, body, status, err := o.send(ctx, rawURL, auth, accept)
	if err != nil {
		return nil, nil, err
	}
	if status == http.StatusUnauthorized {
		auth, err = o.authorize(ctx, r, loc, h.Get("WWW-Authenticate"), scope, key)
		if err != nil {
			return nil, nil, err
		}
		h, body, status, err = o.send(ctx, rawURL, auth, accept)
		if err != nil {
			return nil, nil, err
		}
	}
	if status < 200 || status > 299 {
		return h, nil, &providers.UpstreamError{Provider: ociName, Status: status, Message: ociErrorMessage(body)}
	}
	return h, body, nil
}

func (o *OCI) send(ctx context.Context, rawURL, auth string, accept []string) (http.Header, []byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("User-Agent", providers.UserAgent)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for _, a := range accept {
		req.Header.Add("Accept", a)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%s request failed: %w", ociName, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%s read failed: %w", ociName, err)
	}
	return resp.Header, body, resp.StatusCode, nil
}

func (o *OCI) authorize(ctx context.Context, r *Registry, loc *Location, challenge, scope, key string) (string, error) {
	user, pass, hasCreds, err := r.BasicAuth(loc.Host)
	if err != nil {
		return "", err
	}
	scheme, params := parseChallenge(challenge)
	switch scheme {
	case "basic":
		if !hasCreds {
			return "", &providers.UpstreamError{Provider: ociName, Status: http.StatusUnauthorized, Message: "registry requires credentials"}
		}
		value := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
		o.storeToken(key, value, time.Hour)
		return value, nil
	case "bearer":
	default:
		return "", &providers.UpstreamError{Provider: ociName, Status: http.StatusUnauthorized, Message: "unsupported authentication challenge"}
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return "", &providers.UpstreamError{Provider: ociName, Status: http.StatusUnauthorized, Message: "invalid token realm in authentication challenge"}
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		scope = s
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	realm.RawQuery = q.Encode()
	h := http.Header{}
	if hasCreds {
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if _, err := providers.Do(ctx, o.client, ociName+" auth", providers.Request{URL: realm.String(), Header: h, Out: &tok}); err != nil {
		return "", err
	}
	value := tok.Token
	if value == "" {
		value = tok.AccessToken
	}
	if value == "" {
		return "", &providers.UpstreamError{Provider: ociName, Status: http.StatusUnauthorized, Message: "token endpoint returned no token"}
	}
	ttl := defaultTokenExpiry
	if tok.ExpiresIn > 0 {
		ttl = time.Duration(tok.ExpiresIn) * time.Second
	}
	o.storeToken(key, "Bearer "+value, ttl-tokenExpiryMargin)
	return "Bearer " + value, nil
}

func (o *OCI) storeToken(key, value string, ttl time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.tokens) >= maxCacheEntries {
		o.tokens = map[string]cachedToken{}
	}
	o.tokens[key] = cachedToken{value: value, expires: time.Now().Add(ttl)}
}

func (o *OCI) cachedToken(key string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.tokens[key]; ok && time.Now().Before(t.expires) {
		return t.value
	}
	return ""
}

// tokenKey includes a credentials fingerprint so edited credentials never
// reuse a token issued for the old ones.
func (o *OCI) tokenKey(r *Registry, loc *Location, scope string) string {
	keys := r.CredentialKeys()
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, r.Credentials[k])
	}
	return r.Name + "|" + loc.Host + "|" + scope + "|" + hex.EncodeToString(h.Sum(nil)[:8])
}

var challengeParamRe = regexp.MustCompile(`([A-Za-z_]+)=(?:"([^"]*)"|([^,\s]*))`)

// parseChallenge parses a WWW-Authenticate header into a lowercase scheme
// and its parameters.
func parseChallenge(h string) (string, map[string]string) {
	h = strings.TrimSpace(h)
	scheme, rest, _ := strings.Cut(h, " ")
	params := map[string]string{}
	for _, m := range challengeParamRe.FindAllStringSubmatch(rest, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		params[strings.ToLower(m[1])] = v
	}
	return strings.ToLower(scheme), params
}

var linkRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

// nextLink resolves a Link rel="next" header against the registry origin and
// drops links to other origins.
func nextLink(loc *Location, h http.Header) string {
	m := linkRe.FindStringSubmatch(h.Get("Link"))
	if m == nil {
		return ""
	}
	base, _ := url.Parse(loc.Base() + "/")
	u, err := base.Parse(m[1])
	if err != nil || u.Scheme != loc.Scheme || u.Host != loc.Host {
		return ""
	}
	return u.String()
}

// ociErrorMessage extracts messages from {"errors":[{"code","message"}]}.
func ociErrorMessage(body []byte) string {
	var e struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
		msgs := []string{}
		for _, x := range e.Errors {
			msg := x.Message
			if msg == "" {
				msg = x.Code
			}
			msgs = append(msgs, msg)
		}
		sort.Strings(msgs)
		return strings.Join(msgs, "; ")
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
