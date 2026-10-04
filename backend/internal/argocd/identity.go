// Package argocd contains helpers for requests proxied by the Argo CD API server.
package argocd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Header names set by the Argo CD proxy extension.
// See https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/proxy-extensions/
const (
	HeaderApplicationName   = "Argocd-Application-Name"
	HeaderProjectName       = "Argocd-Project-Name"
	HeaderNamespace         = "Argocd-Namespace"
	HeaderUsername          = "Argocd-Username"
	HeaderUserID            = "Argocd-User-Id"
	HeaderUserGroups        = "Argocd-User-Groups"
	HeaderTargetClusterName = "Argocd-Target-Cluster-Name"
	HeaderTargetClusterURL  = "Argocd-Target-Cluster-URL"
)

// Identity describes the authenticated Argo CD user and the Application
// in whose context the request was authorized.
type Identity struct {
	Username             string   `json:"username"`
	UserID               string   `json:"userId"`
	Groups               []string `json:"groups"`
	ArgoCDNamespace      string   `json:"argocdNamespace"`
	ApplicationNamespace string   `json:"applicationNamespace"`
	ApplicationName      string   `json:"applicationName"`
	ProjectName          string   `json:"projectName"`
	TargetClusterName    string   `json:"targetClusterName,omitempty"`
	TargetClusterURL     string   `json:"targetClusterURL,omitempty"`
}

// ErrMissingHeader is returned when a mandatory Argo CD header is absent.
var ErrMissingHeader = errors.New("missing mandatory Argo CD header")

// IdentityFromRequest extracts the identity from Argo CD proxy headers.
// Only call this after the request has been verified to come from Argo CD.
func IdentityFromRequest(r *http.Request) (*Identity, error) {
	appHeader := strings.TrimSpace(r.Header.Get(HeaderApplicationName))
	if appHeader == "" {
		return nil, fmt.Errorf("%w: %s", ErrMissingHeader, HeaderApplicationName)
	}
	appNamespace, appName, err := ParseApplicationHeader(appHeader)
	if err != nil {
		return nil, err
	}

	project := strings.TrimSpace(r.Header.Get(HeaderProjectName))
	if project == "" {
		return nil, fmt.Errorf("%w: %s", ErrMissingHeader, HeaderProjectName)
	}

	return &Identity{
		Username:             r.Header.Get(HeaderUsername),
		UserID:               r.Header.Get(HeaderUserID),
		Groups:               splitGroups(r.Header.Get(HeaderUserGroups)),
		ArgoCDNamespace:      r.Header.Get(HeaderNamespace),
		ApplicationNamespace: appNamespace,
		ApplicationName:      appName,
		ProjectName:          project,
		TargetClusterName:    r.Header.Get(HeaderTargetClusterName),
		TargetClusterURL:     r.Header.Get(HeaderTargetClusterURL),
	}, nil
}

// ParseApplicationHeader parses the "<namespace>:<app-name>" header value.
func ParseApplicationHeader(v string) (namespace, name string, err error) {
	ns, n, ok := strings.Cut(v, ":")
	if !ok || ns == "" || n == "" || strings.Contains(n, ":") {
		return "", "", fmt.Errorf("invalid %s header %q: expected <namespace>:<app-name>", HeaderApplicationName, v)
	}
	return ns, n, nil
}

// Argo CD joins groups with "," (server/extension/extension.go).
func splitGroups(v string) []string {
	groups := []string{}
	for _, g := range strings.Split(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}
