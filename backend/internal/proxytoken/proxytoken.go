// Package proxytoken makes sure the Secret holding the shared Argo CD -> Zea
// proxy token exists. It runs once per install/upgrade (Helm hook / Argo CD
// PreSync Job) so the token is generated in the cluster and never has to be
// rendered by Helm or stored in git.
package proxytoken

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// PartOfLabel lets Argo CD resolve "$<secret>:<key>" references to the
	// Secret from extension.config.
	PartOfLabel = "app.kubernetes.io/part-of"
	PartOfValue = "argocd"

	managedByLabel = "argocd-zea.io/managed-by"
	tokenBytes     = 32
)

// Result describes what Ensure changed.
type Result string

const (
	Created   Result = "created"
	Updated   Result = "updated"
	Unchanged Result = "unchanged"
)

// Ensure creates the Secret with a random token when it does not exist. An
// existing token is never replaced; only a missing key or the Argo CD label
// are added.
func Ensure(ctx context.Context, kube kubernetes.Interface, namespace, name, key string) (Result, error) {
	if namespace == "" || name == "" || key == "" {
		return "", fmt.Errorf("namespace, name and key are required")
	}
	secrets := kube.CoreV1().Secrets(namespace)

	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		token, err := newToken()
		if err != nil {
			return "", err
		}
		s = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{PartOfLabel: PartOfValue, managedByLabel: "zea"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: []byte(token)},
		}
		_, err = secrets.Create(ctx, s, metav1.CreateOptions{})
		if err == nil {
			return Created, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
		}
		// Lost a race with a parallel run; fall through to the update path.
		s, err = secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}

	changed := false
	if s.Labels[PartOfLabel] != PartOfValue {
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		s.Labels[PartOfLabel] = PartOfValue
		changed = true
	}
	if len(s.Data[key]) == 0 {
		token, err := newToken()
		if err != nil {
			return "", err
		}
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		s.Data[key] = []byte(token)
		changed = true
	}
	if !changed {
		return Unchanged, nil
	}
	if _, err := secrets.Update(ctx, s, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update secret %s/%s: %w", namespace, name, err)
	}
	return Updated, nil
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
