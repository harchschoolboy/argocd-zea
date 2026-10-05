package registries

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// DigitalOceanHost is the DigitalOcean Container Registry host.
const DigitalOceanHost = "registry.digitalocean.com"

const doName = "DigitalOcean"

// DigitalOcean lists images through the DigitalOcean API, which, unlike the
// registry API, reports push time and size for every tag.
type DigitalOcean struct {
	client *http.Client
	// APIBase is the DigitalOcean API origin; tests override it.
	APIBase string
}

// NewDigitalOcean returns a DigitalOcean client.
func NewDigitalOcean(client *http.Client) *DigitalOcean {
	return &DigitalOcean{client: client, APIBase: "https://api.digitalocean.com"}
}

func (d *DigitalOcean) Info() KindInfo {
	return KindInfo{
		ID:         "digitalocean",
		Name:       "DigitalOcean Container Registry",
		URLExample: DigitalOceanHost + "/<registry>",
		URLHelp:    "Registry host and your registry name.",
		CredentialModes: []providers.CredentialMode{
			{
				ID:    "token",
				Label: "API token",
				Help:  "A DigitalOcean API token with read access to the container registry (scope registry:read).",
				Fields: []providers.CredentialField{
					{Key: CredToken, Label: "API token", Secret: true},
				},
			},
			{
				ID:    "dockerconfig",
				Label: "Docker config",
				Help:  "A .dockerconfigjson whose " + DigitalOceanHost + " password is a DigitalOcean API token (as written by doctl registry login).",
				Fields: []providers.CredentialField{
					{Key: CredDockerConfig, Label: ".dockerconfigjson", Secret: true, Multiline: true},
				},
			},
		},
	}
}

func (d *DigitalOcean) Validate(r *Registry) error {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return err
	}
	if loc.Host != DigitalOceanHost || loc.Namespace == "" || strings.Contains(loc.Namespace, "/") {
		return fmt.Errorf("DigitalOcean registry url must look like %s/<registry>, got %q", DigitalOceanHost, r.URL)
	}
	return nil
}

func (d *DigitalOcean) token(r *Registry) (string, error) {
	if t := r.Credentials[CredToken]; t != "" {
		return t, nil
	}
	_, pass, ok, err := r.BasicAuth(DigitalOceanHost)
	if err != nil {
		return "", err
	}
	if !ok || pass == "" {
		return "", errors.New("DigitalOcean registry needs an API token")
	}
	return pass, nil
}

type doPages struct {
	Links struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

type doRepositories struct {
	Repositories []struct {
		Name           string `json:"name"`
		TagCount       int    `json:"tag_count"`
		LatestManifest *struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"latest_manifest"`
	} `json:"repositories"`
	doPages
}

type doTags struct {
	Tags []struct {
		Tag                 string    `json:"tag"`
		ManifestDigest      string    `json:"manifest_digest"`
		CompressedSizeBytes int64     `json:"compressed_size_bytes"`
		UpdatedAt           time.Time `json:"updated_at"`
	} `json:"tags"`
	doPages
}

func (d *DigitalOcean) ListRepositories(ctx context.Context, r *Registry) ([]Repository, error) {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return nil, err
	}
	next := fmt.Sprintf("%s/v2/registry/%s/repositoriesV2?per_page=100", d.APIBase, url.PathEscape(loc.Namespace))
	out := []Repository{}
	for page := 0; next != "" && page < maxPages; page++ {
		var body doRepositories
		if err := d.get(ctx, r, next, &body); err != nil {
			return nil, err
		}
		for _, repo := range body.Repositories {
			item := Repository{Name: repo.Name, TagCount: repo.TagCount}
			if repo.LatestManifest != nil {
				item.UpdatedAt = repo.LatestManifest.UpdatedAt
			}
			out = append(out, item)
		}
		next = d.sameOrigin(body.Links.Pages.Next)
	}
	return out, nil
}

func (d *DigitalOcean) ListTags(ctx context.Context, r *Registry, repo string) ([]Tag, error) {
	loc, err := ParseURL(r.URL)
	if err != nil {
		return nil, err
	}
	next := fmt.Sprintf("%s/v2/registry/%s/repositories/%s/tags?per_page=100",
		d.APIBase, url.PathEscape(loc.Namespace), url.PathEscape(repo))
	out := []Tag{}
	for page := 0; next != "" && page < maxPages; page++ {
		var body doTags
		if err := d.get(ctx, r, next, &body); err != nil {
			return nil, err
		}
		for _, t := range body.Tags {
			out = append(out, Tag{Name: t.Tag, Digest: t.ManifestDigest, SizeBytes: t.CompressedSizeBytes, PushedAt: t.UpdatedAt})
		}
		next = d.sameOrigin(body.Links.Pages.Next)
	}
	return out, nil
}

func (d *DigitalOcean) get(ctx context.Context, r *Registry, rawURL string, out any) error {
	tok, err := d.token(r)
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	h.Set("Accept", "application/json")
	_, err = providers.Do(ctx, d.client, doName, providers.Request{URL: rawURL, Header: h, Out: out})
	return err
}

// sameOrigin keeps pagination links on the API origin so the token is never
// sent elsewhere.
func (d *DigitalOcean) sameOrigin(next string) string {
	if next == "" {
		return ""
	}
	base, err1 := url.Parse(d.APIBase)
	u, err2 := url.Parse(next)
	if err1 != nil || err2 != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return ""
	}
	return next
}
