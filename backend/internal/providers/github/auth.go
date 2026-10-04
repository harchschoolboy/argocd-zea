package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// Credential keys stored in Connection Secrets.
const (
	KeyAppID          = "githubAppID"
	KeyInstallationID = "githubAppInstallationID"
	KeyPrivateKey     = "githubAppPrivateKey"
	KeyToken          = "token"

	modeApp   = "app"
	modeToken = "token"
)

// Permissions requested for installation tokens.
type Permissions map[string]string

// readPermissions is enough to list branches and read workflow definitions.
var readPermissions = Permissions{"metadata": "read", "contents": "read", "actions": "read"}

// writePermissions also allows starting, cancelling and rerunning workflows.
var writePermissions = Permissions{"metadata": "read", "contents": "read", "actions": "write"}

// tokenRefreshMargin renews installation tokens before they expire.
const tokenRefreshMargin = 5 * time.Minute

type cachedToken struct {
	token     string
	expiresAt time.Time
}

type tokenCache struct {
	mu    sync.Mutex
	items map[string]cachedToken
}

// token returns a bearer token for the Connection, scoped to its repository
// and the given permissions when a GitHub App is used.
func (p *Provider) token(ctx context.Context, c *connections.Connection, t *target, perms Permissions) (string, error) {
	if tok := c.Credentials[KeyToken]; tok != "" {
		return tok, nil
	}
	appID := strings.TrimSpace(c.Credentials[KeyAppID])
	instID := strings.TrimSpace(c.Credentials[KeyInstallationID])
	key := strings.Join([]string{c.Name, t.apiBase, appID, instID, t.owner + "/" + t.repo, permKey(perms)}, "|")

	now := p.now()
	p.tokens.mu.Lock()
	if ct, ok := p.tokens.items[key]; ok && now.Add(tokenRefreshMargin).Before(ct.expiresAt) {
		p.tokens.mu.Unlock()
		return ct.token, nil
	}
	p.tokens.mu.Unlock()

	pk, err := parsePrivateKey(c.Credentials[KeyPrivateKey])
	if err != nil {
		return "", err
	}
	jwt, err := appJWT(appID, pk, now)
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_, err = providers.Do(ctx, p.client, providerName, providers.Request{
		Method: http.MethodPost,
		URL:    fmt.Sprintf("%s/app/installations/%s/access_tokens", t.apiBase, instID),
		Header: apiHeaders(jwt),
		Body: map[string]any{
			"repositories": []string{t.repo},
			"permissions":  perms,
		},
		Out: &out,
	})
	if err != nil {
		return "", fmt.Errorf("create GitHub App installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}

	p.tokens.mu.Lock()
	for k, v := range p.tokens.items {
		if !now.Before(v.expiresAt) {
			delete(p.tokens.items, k)
		}
	}
	p.tokens.items[key] = cachedToken{token: out.Token, expiresAt: out.ExpiresAt}
	p.tokens.mu.Unlock()
	return out.Token, nil
}

func permKey(perms Permissions) string {
	keys := make([]string, 0, len(perms))
	for k, v := range perms {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// appJWT builds the short-lived RS256 JWT that authenticates as the App.
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parsePrivateKey accepts PKCS#1 or PKCS#8 PEM. Keys pasted with literal "\n"
// sequences (common when copied from JSON or env files) are accepted too.
func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "\n") && strings.Contains(raw, `\n`) {
		raw = strings.ReplaceAll(raw, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("githubAppPrivateKey is not a PEM-encoded key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("githubAppPrivateKey is not a valid RSA private key")
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("githubAppPrivateKey must be an RSA key")
	}
	return k, nil
}
