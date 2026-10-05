// Package registries stores container registries as labelled Kubernetes
// Secrets and lists their repositories and tags.
package registries

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

const (
	// SecretTypeRegistry is the value of connections.LabelSecretType for registries.
	SecretTypeRegistry = "registry"
	// SecretNamePrefix is used for Secrets created through the UI.
	SecretNamePrefix = "zea-registry-"

	KeyName = "name"
	KeyKind = "kind"
	KeyURL  = "url"

	CredUsername     = "username"
	CredPassword     = "password"
	CredToken        = "token"
	CredDockerConfig = ".dockerconfigjson"
)

var (
	// ErrNotFound is returned when a registry does not exist.
	ErrNotFound = errors.New("registry not found")
	// ErrAlreadyExists is returned when creating a duplicate registry.
	ErrAlreadyExists = errors.New("registry already exists")
	// ErrReadOnly is returned when modifying a declaratively managed registry.
	ErrReadOnly = errors.New("registry is managed declaratively and is read-only in Zea")
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Registry is a container registry with read credentials.
type Registry struct {
	Name string
	Kind string
	// URL is the registry host with an optional namespace, for example
	// "registry.digitalocean.com/team". A scheme is optional (https default).
	URL string
	// Credentials holds secret fields. Never sent to clients.
	Credentials map[string]string
	// Editable is false for declaratively managed registries.
	Editable bool
	// SecretName is the backing Secret's name.
	SecretName string
}

// Validate checks kind-independent fields.
func (r *Registry) Validate() error {
	if len(r.Name) > 50 || !nameRe.MatchString(r.Name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and '-', max 50 characters", r.Name)
	}
	if r.Kind == "" {
		return errors.New("kind is required")
	}
	if _, err := ParseURL(r.URL); err != nil {
		return err
	}
	for k := range r.Credentials {
		if isReservedKey(k) {
			return fmt.Errorf("credential key %q is reserved", k)
		}
	}
	return nil
}

// CredentialKeys returns the names of credential fields that are set.
func (r *Registry) CredentialKeys() []string {
	keys := make([]string, 0, len(r.Credentials))
	for k, v := range r.Credentials {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Location is a parsed registry URL.
type Location struct {
	Scheme string
	Host   string
	// Namespace is the path below the host without slashes at either end.
	Namespace string
}

// ParseURL parses "host[:port][/namespace]" with an optional http(s) scheme.
func ParseURL(raw string) (*Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required")
	}
	withScheme := raw
	if !strings.Contains(raw, "://") {
		withScheme = "https://" + raw
	}
	u, err := url.Parse(withScheme)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %v", raw, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("url %q must use http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url %q has no host", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("url %q must not contain credentials, query or fragment", raw)
	}
	return &Location{Scheme: u.Scheme, Host: strings.ToLower(u.Host), Namespace: strings.Trim(u.Path, "/")}, nil
}

// Base returns the registry API origin, e.g. "https://ghcr.io".
func (l *Location) Base() string {
	return l.Scheme + "://" + l.Host
}

// Path returns the full repository path of repo below the namespace.
func (l *Location) Path(repo string) string {
	if l.Namespace == "" {
		return repo
	}
	return l.Namespace + "/" + repo
}

// ImageRef returns the pullable image reference of repo without a tag.
func (l *Location) ImageRef(repo string) string {
	return l.Host + "/" + l.Path(repo)
}

// BasicAuth returns docker login credentials, if any are configured.
func (r *Registry) BasicAuth(host string) (user, pass string, ok bool, err error) {
	c := r.Credentials
	switch {
	case c[CredUsername] != "" || c[CredPassword] != "":
		return c[CredUsername], c[CredPassword], true, nil
	case c[CredToken] != "":
		return c[CredToken], c[CredToken], true, nil
	case c[CredDockerConfig] != "":
		user, pass, err = DockerConfigAuth(c[CredDockerConfig], host)
		if err != nil {
			return "", "", false, err
		}
		return user, pass, true, nil
	}
	return "", "", false, nil
}

// DockerConfigAuth returns the credentials for host from a .dockerconfigjson
// document ({"auths": {...}}).
func DockerConfigAuth(raw, host string) (string, string, error) {
	var cfg struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", "", fmt.Errorf("invalid .dockerconfigjson: %v", err)
	}
	want := normalizeAuthHost(host)
	for key, a := range cfg.Auths {
		if normalizeAuthHost(key) != want {
			continue
		}
		if a.Username != "" || a.Password != "" {
			return a.Username, a.Password, nil
		}
		dec, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil {
			return "", "", fmt.Errorf(".dockerconfigjson: invalid auth for %s", key)
		}
		user, pass, found := strings.Cut(string(dec), ":")
		if !found {
			return "", "", fmt.Errorf(".dockerconfigjson: auth for %s is not user:password", key)
		}
		return user, pass, nil
	}
	return "", "", fmt.Errorf(".dockerconfigjson has no entry for %s", host)
}

// normalizeAuthHost reduces docker config keys like "https://host/v1/" to
// the bare host and folds Docker Hub aliases.
func normalizeAuthHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return s
}

// ValidateCredentials checks credentials against the modes of a kind. A
// mode without fields allows anonymous access when nothing is set.
func ValidateCredentials(modes []providers.CredentialMode, creds map[string]string) error {
	anySet := false
	for _, v := range creds {
		if v != "" {
			anySet = true
		}
	}
	withFields := []providers.CredentialMode{}
	anonymous := false
	for _, m := range modes {
		if len(m.Fields) == 0 {
			anonymous = true
		} else {
			withFields = append(withFields, m)
		}
	}
	if !anySet && anonymous {
		return nil
	}
	_, err := providers.ValidateCredentials(withFields, creds)
	return err
}

func isReservedKey(k string) bool {
	switch k {
	case KeyName, KeyKind, KeyURL:
		return true
	}
	return false
}
