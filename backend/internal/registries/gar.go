package registries

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/google/externalaccount"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// GARHostSuffix ends every Artifact Registry Docker host, e.g.
// "europe-west1-docker.pkg.dev".
const GARHostSuffix = "-docker.pkg.dev"

const (
	garName  = "Artifact Registry"
	garScope = "https://www.googleapis.com/auth/cloud-platform.read-only"
	// garErrorTTL limits how often a failed credential lookup is retried;
	// probing the metadata server off GCP is slow.
	garErrorTTL = time.Minute
)

// GAR lists images of a Google Artifact Registry Docker repository through
// the Artifact Registry API, which reports push time and size per version.
type GAR struct {
	client *http.Client
	// APIBase is the Artifact Registry API origin; tests override it.
	APIBase string
	// FindDefault looks up Application Default Credentials; tests override it.
	FindDefault func(ctx context.Context, scopes ...string) (*google.Credentials, error)
	// FromKey parses a service account key; tests override it.
	FromKey func(ctx context.Context, key []byte, scopes ...string) (*google.Credentials, error)
	// STSURL and IAMCredentialsBase are the Workload Identity Federation
	// endpoints; tests override them.
	STSURL             string
	IAMCredentialsBase string

	// subjectTokens enables the Workload Identity Federation mode.
	subjectTokens SubjectTokens

	mu    sync.Mutex
	cache map[string]garSource
}

type garSource struct {
	ts      oauth2.TokenSource
	err     error
	expires time.Time
}

// NewGAR returns an Artifact Registry client.
func NewGAR(client *http.Client) *GAR {
	return &GAR{
		client:      client,
		APIBase:     "https://artifactregistry.googleapis.com",
		FindDefault: google.FindDefaultCredentials,
		FromKey: func(ctx context.Context, key []byte, scopes ...string) (*google.Credentials, error) {
			return google.CredentialsFromJSONWithType(ctx, key, google.ServiceAccount, scopes...)
		},
		STSURL:             "https://sts.googleapis.com/v1/token",
		IAMCredentialsBase: "https://iamcredentials.googleapis.com",
		cache:              map[string]garSource{},
	}
}

// SetSubjectTokens enables the per-registry Workload Identity Federation
// mode, which exchanges tokens of the Zea service account.
func (g *GAR) SetSubjectTokens(t SubjectTokens) {
	g.subjectTokens = t
}

func (g *GAR) Info() KindInfo {
	modes := []providers.CredentialMode{}
	if g.subjectTokens != nil {
		modes = append(modes, providers.CredentialMode{
			ID:    "wif",
			Label: "Workload Identity Federation",
			Help:  "Zea exchanges a token of its Kubernetes service account for a Google access token. The provider must trust this cluster's service account issuer; grant roles/artifactregistry.reader to the impersonated service account, or without one to the principal .../subject/system:serviceaccount:<namespace>:<name> of the Zea service account.",
			Fields: []providers.CredentialField{
				{Key: CredWIFProvider, Label: "Workload identity provider", Help: "//iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>"},
				{Key: CredImpersonate, Label: "Service account to impersonate", Help: "<name>@<project>.iam.gserviceaccount.com; the federated principal needs roles/iam.workloadIdentityUser on it.", Optional: true},
			},
		})
	}
	modes = append(modes,
		providers.CredentialMode{
			ID:     "workload",
			Label:  "Pod identity",
			Help:   "The Zea pod's Google identity: GKE Workload Identity, Workload Identity Federation configured in the chart (gcp.workloadIdentityFederation) or GOOGLE_APPLICATION_CREDENTIALS. It needs roles/artifactregistry.reader.",
			Fields: []providers.CredentialField{},
		},
	)
	return KindInfo{
		ID:         "gar",
		Name:       "Google Artifact Registry",
		URLExample: "<location>" + GARHostSuffix + "/<project>/<repository>",
		URLHelp:    "Docker repository URL; a trailing path limits the list to images below it.",
		CredentialModes: append(modes,
			providers.CredentialMode{
				ID:    "serviceaccount",
				Label: "Service account key",
				Help:  "A JSON key of a service account with roles/artifactregistry.reader. Prefer Workload Identity Federation: keys are long-lived secrets.",
				Fields: []providers.CredentialField{
					{Key: CredServiceAccountKey, Label: "Service account key (JSON)", Secret: true, Multiline: true},
				},
			},
			PullSecretMode(),
		),
	}
}

// garRepo is a parsed Artifact Registry URL.
type garRepo struct {
	host, location, project, repository, prefix string
}

func parseGAR(raw string) (*garRepo, error) {
	loc, err := ParseURL(raw)
	if err != nil {
		return nil, err
	}
	location := strings.TrimSuffix(loc.Host, GARHostSuffix)
	parts := strings.SplitN(loc.Namespace, "/", 3)
	if loc.Scheme != "https" || location == "" || location == loc.Host || len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("Artifact Registry url must look like <location>%s/<project>/<repository>, got %q", GARHostSuffix, raw)
	}
	g := &garRepo{host: loc.Host, location: location, project: parts[0], repository: parts[1]}
	if len(parts) == 3 {
		g.prefix = strings.Trim(parts[2], "/")
	}
	return g, nil
}

func (g *GAR) Validate(r *Registry) error {
	if _, err := parseGAR(r.URL); err != nil {
		return err
	}
	if err := validateWIF(r); err != nil {
		return err
	}
	if key := r.Credentials[CredServiceAccountKey]; key != "" {
		if _, err := g.FromKey(context.Background(), []byte(key), garScope); err != nil {
			return fmt.Errorf("invalid service account key: %v", err)
		}
	}
	return nil
}

func (g *GAR) repoPath(p *garRepo) string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/repositories/%s",
		g.APIBase, url.PathEscape(p.project), url.PathEscape(p.location), url.PathEscape(p.repository))
}

type garPackages struct {
	Packages []struct {
		Name       string    `json:"name"`
		UpdateTime time.Time `json:"updateTime"`
	} `json:"packages"`
	NextPageToken string `json:"nextPageToken"`
}

type garVersions struct {
	Versions []struct {
		Name        string    `json:"name"`
		CreateTime  time.Time `json:"createTime"`
		UpdateTime  time.Time `json:"updateTime"`
		RelatedTags []struct {
			Name string `json:"name"`
		} `json:"relatedTags"`
		Metadata struct {
			ImageSizeBytes string `json:"imageSizeBytes"`
		} `json:"metadata"`
	} `json:"versions"`
	NextPageToken string `json:"nextPageToken"`
}

// lastSegment returns the unescaped resource ID after marker in name.
func lastSegment(name, marker string) string {
	i := strings.LastIndex(name, marker)
	if i < 0 {
		return ""
	}
	s := name[i+len(marker):]
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func (g *GAR) ListRepositories(ctx context.Context, r *Registry) ([]Repository, error) {
	p, err := parseGAR(r.URL)
	if err != nil {
		return nil, err
	}
	base := g.repoPath(p) + "/packages?pageSize=1000"
	out := []Repository{}
	token := ""
	for page := 0; page < maxPages; page++ {
		u := base
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var body garPackages
		if err := g.get(ctx, r, p, u, &body); err != nil {
			return nil, err
		}
		for _, pkg := range body.Packages {
			name := lastSegment(pkg.Name, "/packages/")
			if p.prefix != "" {
				rest, ok := strings.CutPrefix(name, p.prefix+"/")
				if !ok {
					continue
				}
				name = rest
			}
			if name != "" {
				out = append(out, Repository{Name: name, UpdatedAt: pkg.UpdateTime})
			}
		}
		if token = body.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (g *GAR) ListTags(ctx context.Context, r *Registry, repo string) ([]Tag, error) {
	p, err := parseGAR(r.URL)
	if err != nil {
		return nil, err
	}
	pkg := repo
	if p.prefix != "" {
		pkg = p.prefix + "/" + repo
	}
	base := g.repoPath(p) + "/packages/" + url.PathEscape(pkg) + "/versions?view=FULL&pageSize=1000"
	out := []Tag{}
	token := ""
	for page := 0; page < maxPages; page++ {
		u := base
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var body garVersions
		if err := g.get(ctx, r, p, u, &body); err != nil {
			return nil, err
		}
		for _, v := range body.Versions {
			size, _ := strconv.ParseInt(v.Metadata.ImageSizeBytes, 10, 64)
			pushed := v.CreateTime
			if pushed.IsZero() {
				pushed = v.UpdateTime
			}
			digest := lastSegment(v.Name, "/versions/")
			for _, t := range v.RelatedTags {
				if name := lastSegment(t.Name, "/tags/"); name != "" {
					out = append(out, Tag{Name: name, Digest: digest, SizeBytes: size, PushedAt: pushed})
				}
			}
		}
		if token = body.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (g *GAR) get(ctx context.Context, r *Registry, p *garRepo, rawURL string, out any) error {
	ts, err := g.tokenSource(r, p)
	if err != nil {
		return err
	}
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("%s: getting a Google access token failed: %v", garName, err)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok.AccessToken)
	h.Set("Accept", "application/json")
	_, err = providers.Do(ctx, g.client, garName, providers.Request{URL: rawURL, Header: h, Out: out})
	return err
}

// tokenSource returns a cached token source for the registry credentials:
// a service account key, a pull secret (whose docker login carries a key or
// an access token) or Application Default Credentials.
func (g *GAR) tokenSource(r *Registry, p *garRepo) (oauth2.TokenSource, error) {
	if r.Credentials[CredWIFProvider] != "" {
		return g.wifTokenSource(r)
	}
	key := []byte(r.Credentials[CredServiceAccountKey])
	if len(key) == 0 && r.Credentials[CredDockerConfig] != "" {
		user, pass, err := DockerConfigAuth(r.Credentials[CredDockerConfig], p.host)
		if err != nil {
			return nil, err
		}
		switch user {
		case "_json_key":
			key = []byte(pass)
		case "_json_key_base64":
			if key, err = base64.StdEncoding.DecodeString(pass); err != nil {
				return nil, fmt.Errorf("pull secret: invalid _json_key_base64 password for %s", p.host)
			}
		case "oauth2accesstoken":
			// Short-lived; whoever maintains the Secret refreshes it.
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pass}), nil
		default:
			return nil, fmt.Errorf("pull secret for %s must log in as _json_key, _json_key_base64 or oauth2accesstoken, not %q", p.host, user)
		}
	}

	id := "adc"
	if len(key) > 0 {
		sum := sha256.Sum256(key)
		id = "key:" + hex.EncodeToString(sum[:])
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.cache[id]; ok && (c.err == nil || time.Now().Before(c.expires)) {
		return c.ts, c.err
	}

	// Token sources outlive the request, so they must not keep its context.
	bg := context.Background()
	var creds *google.Credentials
	var err error
	if len(key) > 0 {
		creds, err = g.FromKey(bg, key, garScope)
		if err != nil {
			err = fmt.Errorf("invalid service account key: %v", err)
		}
	} else {
		creds, err = g.FindDefault(bg, garScope)
		if err != nil {
			err = fmt.Errorf("%s: no Google credentials found for the Zea pod; set up Workload Identity or use a service account key (%v)", garName, err)
		}
	}
	if err != nil {
		g.cache[id] = garSource{err: err, expires: time.Now().Add(garErrorTTL)}
		return nil, err
	}
	if creds == nil || creds.TokenSource == nil {
		return nil, errors.New("Google credentials have no token source")
	}
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	g.cache[id] = garSource{ts: ts}
	return ts, nil
}

// wifTokenSource exchanges Zea service account tokens through Workload
// Identity Federation, optionally impersonating a Google service account.
func (g *GAR) wifTokenSource(r *Registry) (oauth2.TokenSource, error) {
	if g.subjectTokens == nil {
		return nil, errors.New("Workload Identity Federation is not enabled in Zea (chart value registries.tokenRequest)")
	}
	if err := validateWIF(r); err != nil {
		return nil, err
	}
	provider, sa := r.Credentials[CredWIFProvider], r.Credentials[CredImpersonate]
	id := "wif:" + provider + "|" + sa
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.cache[id]; ok && c.err == nil {
		return c.ts, nil
	}
	cfg := externalaccount.Config{
		Audience:         provider,
		SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt",
		TokenURL:         g.STSURL,
		Scopes:           []string{garScope},
		// The provider's default allowed audience.
		SubjectTokenSupplier: wifSupplier{tokens: g.subjectTokens, audience: "https:" + provider},
	}
	if sa != "" {
		cfg.ServiceAccountImpersonationURL = fmt.Sprintf("%s/v1/projects/-/serviceAccounts/%s:generateAccessToken", g.IAMCredentialsBase, sa)
	}
	ts, err := externalaccount.NewTokenSource(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: Workload Identity Federation: %v", garName, err)
	}
	ts = oauth2.ReuseTokenSource(nil, ts)
	g.cache[id] = garSource{ts: ts}
	return ts, nil
}
