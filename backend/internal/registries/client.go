package registries

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// Repository is an image repository relative to the registry namespace.
type Repository struct {
	Name      string    `json:"name"`
	TagCount  int       `json:"tagCount,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// Tag is one tag of a repository. Fields other than Name are optional.
type Tag struct {
	Name      string    `json:"name"`
	Digest    string    `json:"digest,omitempty"`
	SizeBytes int64     `json:"sizeBytes,omitempty"`
	PushedAt  time.Time `json:"pushedAt,omitzero"`
}

// KindInfo describes a registry kind for the UI.
type KindInfo struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	URLExample      string                     `json:"urlExample"`
	URLHelp         string                     `json:"urlHelp"`
	CredentialModes []providers.CredentialMode `json:"credentialModes"`
}

// Client lists repositories and tags of one registry kind.
type Client interface {
	Info() KindInfo
	// Validate checks kind-specific fields (URL shape, credentials).
	Validate(r *Registry) error
	// ListRepositories lists repositories below the registry namespace.
	ListRepositories(ctx context.Context, r *Registry) ([]Repository, error)
	// ListTags lists the tags of repo (relative to the namespace).
	ListTags(ctx context.Context, r *Registry, repo string) ([]Tag, error)
}

// TagDescriber is implemented by clients whose ListTags returns bare tag
// names. DescribeTags fills digest, size and push time on a best-effort basis.
type TagDescriber interface {
	DescribeTags(ctx context.Context, r *Registry, repo string, tags []Tag) []Tag
}

// Kinds maps kind IDs to clients.
type Kinds struct {
	byID        map[string]Client
	pullSecrets PullSecretSource
}

// NewKinds registers the given clients.
func NewKinds(cs ...Client) *Kinds {
	k := &Kinds{byID: map[string]Client{}}
	for _, c := range cs {
		k.byID[c.Info().ID] = c
	}
	return k
}

// SetPullSecrets enables pull secret references for kinds that offer them.
func (k *Kinds) SetPullSecrets(src PullSecretSource) {
	k.pullSecrets = src
}

// Get returns the client for kind id. Pull secret references are resolved
// transparently.
func (k *Kinds) Get(id string) (Client, error) {
	c, ok := k.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown registry kind %q", id)
	}
	if !hasPullSecretMode(c.Info()) {
		return c, nil
	}
	w := &pullSecretClient{Client: c, src: k.pullSecrets}
	if _, ok := c.(TagDescriber); ok {
		return &pullSecretDescriber{w}, nil
	}
	return w, nil
}

// Infos returns all kind descriptions sorted by ID. The pull secret mode
// lists the allowed Secrets, or is hidden when none are configured.
func (k *Kinds) Infos() []KindInfo {
	allowed := []string{}
	if k.pullSecrets != nil {
		allowed = k.pullSecrets.Allowed()
	}
	out := make([]KindInfo, 0, len(k.byID))
	for _, c := range k.byID {
		info := c.Info()
		modes := make([]providers.CredentialMode, 0, len(info.CredentialModes))
		for _, m := range info.CredentialModes {
			if m.ID == PullSecretModeID {
				if len(allowed) == 0 {
					continue
				}
				m.Fields = append([]providers.CredentialField(nil), m.Fields...)
				m.Fields[0].Options = allowed
			}
			modes = append(modes, m)
		}
		info.CredentialModes = modes
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Validate runs generic and kind-specific validation.
func (k *Kinds) Validate(r *Registry) error {
	if err := r.Validate(); err != nil {
		return err
	}
	c, err := k.Get(r.Kind)
	if err != nil {
		return err
	}
	if err := ValidateCredentials(c.Info().CredentialModes, r.Credentials); err != nil {
		return err
	}
	if ref := r.Credentials[CredPullSecret]; ref != "" {
		ns, name, _ := ParsePullSecretRef(ref)
		if k.pullSecrets == nil || !isAllowed(k.pullSecrets, ns+"/"+name) {
			return fmt.Errorf("pull secret %s/%s is not listed in the chart value registries.pullSecrets", ns, name)
		}
	}
	return c.Validate(r)
}

// maxPages caps paginated listings.
const maxPages = 20
