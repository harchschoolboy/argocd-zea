// Package connections stores Zea Connections as labelled Kubernetes Secrets,
// mirroring how Argo CD stores repositories.
package connections

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// LabelSecretType marks a Secret as a Zea Connection.
	LabelSecretType = "argocd-zea.io/secret-type"
	// SecretTypeConnection is the value of LabelSecretType for Connections.
	SecretTypeConnection = "connection"
	// LabelManagedBy marks Secrets created through the Zea UI. Secrets without
	// it are managed declaratively (git) and are read-only in the UI.
	LabelManagedBy = "argocd-zea.io/managed-by"
	// ManagedByZea is the value of LabelManagedBy for UI-managed Secrets.
	ManagedByZea = "zea"
	// SecretNamePrefix is used for Secrets created through the UI.
	SecretNamePrefix = "zea-conn-"

	KeyName          = "name"
	KeyProvider      = "provider"
	KeyURL           = "url"
	KeyAPIURL        = "apiURL"
	KeyAllowedGroups = "allowedGroups"
	// KeyImages holds the image sources as a YAML list.
	KeyImages = "images"
)

// MaxImageSources caps the image sources of one Connection.
const MaxImageSources = 20

// maxPatternLen caps image source regular expressions.
const maxPatternLen = 500

// AllGroups in allowedGroups grants access to every user who can use Zea.
const AllGroups = "*"

var (
	// ErrNotFound is returned when a Connection does not exist.
	ErrNotFound = errors.New("connection not found")
	// ErrAlreadyExists is returned when creating a duplicate Connection.
	ErrAlreadyExists = errors.New("connection already exists")
	// ErrReadOnly is returned when modifying a declaratively managed Connection.
	ErrReadOnly = errors.New("connection is managed declaratively and is read-only in Zea")
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Connection is one repository + CI provider + credentials + access rules.
type Connection struct {
	Name          string
	Provider      string
	URL           string
	APIURL        string
	AllowedGroups []string
	// Images selects the container images built from this repository.
	Images []ImageSource
	// ImagesError is set when the stored images key cannot be parsed.
	ImagesError string
	// Credentials holds provider-specific secret fields. Never sent to clients.
	Credentials map[string]string
	// Editable is false for declaratively managed Connections.
	Editable bool
	// SecretName is the backing Secret's name.
	SecretName string
}

// Validate checks provider-independent fields.
func (c *Connection) Validate() error {
	if len(c.Name) > 50 || !nameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and '-', max 50 characters", c.Name)
	}
	if c.Provider == "" {
		return errors.New("provider is required")
	}
	if err := validateHTTPURL("url", c.URL, true); err != nil {
		return err
	}
	if err := validateHTTPURL("apiURL", c.APIURL, false); err != nil {
		return err
	}
	for k := range c.Credentials {
		if isReservedKey(k) {
			return fmt.Errorf("credential key %q is reserved", k)
		}
	}
	if c.ImagesError != "" {
		return fmt.Errorf("images: %s", c.ImagesError)
	}
	return ValidateImageSources(c.Images)
}

// ImageSource selects repositories of a container registry. Repository and
// Tags are regular expressions; a named group "branch" in either of them
// ties an image to the branch it was built from (compared as a slug).
type ImageSource struct {
	Registry   string `json:"registry" yaml:"registry"`
	Repository string `json:"repository" yaml:"repository"`
	Tags       string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// ValidateImageSources checks required fields and that patterns compile.
func ValidateImageSources(srcs []ImageSource) error {
	if len(srcs) > MaxImageSources {
		return fmt.Errorf("at most %d image sources are allowed", MaxImageSources)
	}
	for i, s := range srcs {
		if strings.TrimSpace(s.Registry) == "" {
			return fmt.Errorf("image source %d: registry is required", i+1)
		}
		if strings.TrimSpace(s.Repository) == "" {
			return fmt.Errorf("image source %d: repository pattern is required", i+1)
		}
		for field, p := range map[string]string{"repository": s.Repository, "tags": s.Tags} {
			if len(p) > maxPatternLen {
				return fmt.Errorf("image source %d: %s pattern is longer than %d characters", i+1, field, maxPatternLen)
			}
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("image source %d: invalid %s pattern: %v", i+1, field, err)
			}
		}
	}
	return nil
}

// ParseImageSources reads the YAML (or JSON) list stored under KeyImages.
func ParseImageSources(raw string) ([]ImageSource, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	var out []ImageSource
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid images list: %v", err)
	}
	for i := range out {
		out[i].Registry = strings.TrimSpace(out[i].Registry)
		out[i].Repository = strings.TrimSpace(out[i].Repository)
		out[i].Tags = strings.TrimSpace(out[i].Tags)
	}
	return out, nil
}

// FormatImageSources renders image sources for KeyImages.
func FormatImageSources(srcs []ImageSource) string {
	if len(srcs) == 0 {
		return ""
	}
	b, err := yaml.Marshal(srcs)
	if err != nil {
		return ""
	}
	return string(b)
}

// CredentialKeys returns the names of credential fields that are set.
func (c *Connection) CredentialKeys() []string {
	keys := make([]string, 0, len(c.Credentials))
	for k, v := range c.Credentials {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func validateHTTPURL(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
		return fmt.Errorf("%s %q must be an http(s) URL", field, v)
	}
	return nil
}

func isReservedKey(k string) bool {
	switch k {
	case KeyName, KeyProvider, KeyURL, KeyAPIURL, KeyAllowedGroups, KeyImages:
		return true
	}
	return false
}
