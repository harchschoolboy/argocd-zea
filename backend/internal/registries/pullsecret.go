package registries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// PullSecretModeID is the credential mode that references a pull Secret.
const PullSecretModeID = "pullsecret"

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// PullSecretMode is offered by kinds that accept docker credentials. Its
// field options are filled with the allowed Secrets at runtime.
func PullSecretMode() providers.CredentialMode {
	return providers.CredentialMode{
		ID:    PullSecretModeID,
		Label: "Existing pull secret",
		Help:  "An image pull Secret already in the cluster. Zea reads it on every use, so rotated credentials are picked up. Only Secrets listed in the chart value registries.pullSecrets can be used.",
		Fields: []providers.CredentialField{
			{Key: CredPullSecret, Label: "Pull secret (namespace/name)"},
		},
	}
}

// ParsePullSecretRef splits "namespace/name".
func ParsePullSecretRef(ref string) (string, string, error) {
	ns, name, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || len(ns) > 63 || len(name) > 253 || !dnsLabelRe.MatchString(ns) || !dnsLabelRe.MatchString(name) || strings.Contains(ns, ".") {
		return "", "", fmt.Errorf("invalid pull secret %q: expected <namespace>/<name>", ref)
	}
	return ns, name, nil
}

// PullSecretSource reads allowed image pull Secrets.
type PullSecretSource interface {
	// Allowed returns the "namespace/name" references that may be used.
	Allowed() []string
	// DockerConfig returns the .dockerconfigjson of an allowed Secret.
	DockerConfig(ctx context.Context, ref string) (string, error)
}

// KubePullSecrets reads pull Secrets from the cluster. Only an explicit
// allowlist is readable, mirroring the RBAC the chart grants.
type KubePullSecrets struct {
	client  kubernetes.Interface
	allowed []string
}

// NewKubePullSecrets returns a source for the given "namespace/name" refs;
// invalid refs are dropped.
func NewKubePullSecrets(client kubernetes.Interface, allowed []string) *KubePullSecrets {
	out := []string{}
	seen := map[string]bool{}
	for _, ref := range allowed {
		ns, name, err := ParsePullSecretRef(ref)
		if err != nil {
			continue
		}
		ref = ns + "/" + name
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return &KubePullSecrets{client: client, allowed: out}
}

func (k *KubePullSecrets) Allowed() []string {
	return append([]string(nil), k.allowed...)
}

func (k *KubePullSecrets) DockerConfig(ctx context.Context, ref string) (string, error) {
	ns, name, err := ParsePullSecretRef(ref)
	if err != nil {
		return "", err
	}
	if !isAllowed(k, ns+"/"+name) {
		return "", fmt.Errorf("pull secret %s/%s is not listed in the chart value registries.pullSecrets", ns, name)
	}
	s, err := k.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", fmt.Errorf("pull secret %s/%s does not exist", ns, name)
	case apierrors.IsForbidden(err):
		return "", fmt.Errorf("Zea may not read pull secret %s/%s; add it to registries.pullSecrets of the Zea chart", ns, name)
	case err != nil:
		return "", fmt.Errorf("reading pull secret %s/%s: %w", ns, name, err)
	}
	if v := s.Data[corev1.DockerConfigJsonKey]; len(v) > 0 {
		return string(v), nil
	}
	// Legacy kubernetes.io/dockercfg holds the "auths" map itself.
	if v := s.Data[corev1.DockerConfigKey]; len(v) > 0 {
		var auths map[string]json.RawMessage
		if err := json.Unmarshal(v, &auths); err != nil {
			return "", fmt.Errorf("pull secret %s/%s: invalid .dockercfg: %v", ns, name, err)
		}
		b, _ := json.Marshal(map[string]any{"auths": auths})
		return string(b), nil
	}
	return "", fmt.Errorf("pull secret %s/%s has no %s key", ns, name, corev1.DockerConfigJsonKey)
}

func isAllowed(src PullSecretSource, ref string) bool {
	for _, a := range src.Allowed() {
		if a == ref {
			return true
		}
	}
	return false
}

// hasPullSecretMode reports whether a kind accepts pull secret references.
func hasPullSecretMode(info KindInfo) bool {
	for _, m := range info.CredentialModes {
		if m.ID == PullSecretModeID {
			return true
		}
	}
	return false
}

// pullSecretClient resolves a pull secret reference into docker
// credentials before every call, so clients only see .dockerconfigjson.
type pullSecretClient struct {
	Client
	src PullSecretSource
}

func (p *pullSecretClient) resolve(ctx context.Context, r *Registry) (*Registry, error) {
	ref := r.Credentials[CredPullSecret]
	if ref == "" {
		return r, nil
	}
	if p.src == nil {
		return nil, errors.New("pull secrets are not configured in Zea")
	}
	cfg, err := p.src.DockerConfig(ctx, ref)
	if err != nil {
		return nil, err
	}
	cp := *r
	cp.Credentials = map[string]string{CredDockerConfig: cfg}
	return &cp, nil
}

func (p *pullSecretClient) ListRepositories(ctx context.Context, r *Registry) ([]Repository, error) {
	r, err := p.resolve(ctx, r)
	if err != nil {
		return nil, err
	}
	return p.Client.ListRepositories(ctx, r)
}

func (p *pullSecretClient) ListTags(ctx context.Context, r *Registry, repo string) ([]Tag, error) {
	r, err := p.resolve(ctx, r)
	if err != nil {
		return nil, err
	}
	return p.Client.ListTags(ctx, r, repo)
}

// pullSecretDescriber keeps the TagDescriber capability of the wrapped client.
type pullSecretDescriber struct {
	*pullSecretClient
}

func (p *pullSecretDescriber) DescribeTags(ctx context.Context, r *Registry, repo string, tags []Tag) []Tag {
	r, err := p.resolve(ctx, r)
	if err != nil {
		return tags
	}
	return p.Client.(TagDescriber).DescribeTags(ctx, r, repo, tags)
}
