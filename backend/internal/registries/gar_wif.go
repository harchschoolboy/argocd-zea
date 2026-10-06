package registries

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"golang.org/x/oauth2/google/externalaccount"
)

var (
	wifProviderRe = regexp.MustCompile(`^//iam\.googleapis\.com/projects/[0-9]+/locations/global/workloadIdentityPools/[a-z0-9-]+/providers/[a-z0-9-]+$`)
	gsaEmailRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*@[a-z0-9.-]+\.iam\.gserviceaccount\.com$`)
)

// SubjectTokens issues Kubernetes service account tokens of the Zea pod
// for a given audience.
type SubjectTokens interface {
	Token(ctx context.Context, audience string) (string, error)
}

// KubeServiceAccountTokens requests tokens for one service account through
// the TokenRequest API (serviceaccounts/token, create).
type KubeServiceAccountTokens struct {
	client    kubernetes.Interface
	namespace string
	name      string
}

// NewKubeServiceAccountTokens returns a token source for namespace/name.
func NewKubeServiceAccountTokens(client kubernetes.Interface, namespace, name string) *KubeServiceAccountTokens {
	return &KubeServiceAccountTokens{client: client, namespace: namespace, name: name}
}

// subjectTokenSeconds is the lifetime of requested tokens; they are only
// exchanged once for a Google access token.
const subjectTokenSeconds = 600

func (k *KubeServiceAccountTokens) Token(ctx context.Context, audience string) (string, error) {
	exp := int64(subjectTokenSeconds)
	tr, err := k.client.CoreV1().ServiceAccounts(k.namespace).CreateToken(ctx, k.name, &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{Audiences: []string{audience}, ExpirationSeconds: &exp},
	}, metav1.CreateOptions{})
	switch {
	case apierrors.IsForbidden(err):
		return "", fmt.Errorf("Zea may not request tokens for service account %s/%s; enable registries.tokenRequest in the Zea chart", k.namespace, k.name)
	case err != nil:
		return "", fmt.Errorf("requesting a token for service account %s/%s: %w", k.namespace, k.name, err)
	case tr.Status.Token == "":
		return "", errors.New("the TokenRequest API returned an empty token")
	}
	return tr.Status.Token, nil
}

// wifSupplier feeds Kubernetes tokens to the Google STS exchange.
type wifSupplier struct {
	tokens   SubjectTokens
	audience string
}

func (s wifSupplier) SubjectToken(ctx context.Context, _ externalaccount.SupplierOptions) (string, error) {
	return s.tokens.Token(ctx, s.audience)
}

// validateWIF checks the Workload Identity Federation fields of r.
func validateWIF(r *Registry) error {
	p := r.Credentials[CredWIFProvider]
	if p == "" {
		return nil
	}
	if !wifProviderRe.MatchString(p) {
		return fmt.Errorf("invalid workload identity provider %q: expected //iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>", p)
	}
	if sa := r.Credentials[CredImpersonate]; sa != "" && !gsaEmailRe.MatchString(sa) {
		return fmt.Errorf("invalid service account %q: expected <name>@<project>.iam.gserviceaccount.com", sa)
	}
	return nil
}
