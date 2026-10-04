// Package connections stores Zea Connections as labelled Kubernetes Secrets,
// mirroring how Argo CD stores repositories.
package connections

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
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
)

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
	return nil
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
	case KeyName, KeyProvider, KeyURL, KeyAPIURL, KeyAllowedGroups:
		return true
	}
	return false
}
