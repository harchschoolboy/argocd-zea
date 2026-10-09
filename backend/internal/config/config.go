// Package config loads backend configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings for the Zea backend.
type Config struct {
	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
	// ProxyToken is the shared secret that Argo CD injects into every proxied
	// request (via extension.config headers). Requests without it are rejected,
	// because the Argocd-* identity headers are only trustworthy when the
	// request actually passed through the Argo CD API server.
	ProxyToken string
	// InsecureSkipProxyAuth disables the ProxyToken check. Local development only.
	InsecureSkipProxyAuth bool
	// AnchorApp is the "<namespace>:<name>" of the anchor Application. Every
	// request must be scoped to it; Argo CD has already verified that the user
	// can "get" it, which makes it the gate for using Zea at all.
	AnchorApp string
	// AdminGroups and AdminUsers may manage Connections.
	AdminGroups []string
	AdminUsers  []string
	// ConnectionsNamespace holds Connection Secrets.
	ConnectionsNamespace string
	// RegistryPullSecrets lists the "<namespace>/<name>" image pull Secrets a
	// registry may reference instead of storing its own credentials.
	RegistryPullSecrets []string
	// ServiceAccountNamespace and ServiceAccountName identify the pod's own
	// service account. When set, Zea may request tokens for it (registry
	// Workload Identity Federation).
	ServiceAccountNamespace string
	ServiceAccountName      string
	// HTTPTimeout bounds calls to CI providers.
	HTTPTimeout time.Duration
	// LogLevel controls log verbosity.
	LogLevel slog.Level
	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
	// StreamPollInterval is how often the Stream engine checks active runs.
	StreamPollInterval time.Duration
	// StreamStepTimeout is the default limit of one Stream step.
	StreamStepTimeout time.Duration
	// StreamRunHistory is how many finished runs are kept per Stream.
	StreamRunHistory int
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:              getEnv("ZEA_LISTEN_ADDR", ":8080"),
		ProxyToken:              strings.TrimSpace(os.Getenv("ZEA_PROXY_TOKEN")),
		AnchorApp:               strings.TrimSpace(os.Getenv("ZEA_ANCHOR_APP")),
		AdminGroups:             SplitList(os.Getenv("ZEA_ADMIN_GROUPS")),
		AdminUsers:              SplitList(os.Getenv("ZEA_ADMIN_USERS")),
		ConnectionsNamespace:    strings.TrimSpace(os.Getenv("ZEA_CONNECTIONS_NAMESPACE")),
		RegistryPullSecrets:     SplitList(os.Getenv("ZEA_REGISTRY_PULL_SECRETS")),
		ServiceAccountNamespace: strings.TrimSpace(os.Getenv("ZEA_SERVICE_ACCOUNT_NAMESPACE")),
		ServiceAccountName:      strings.TrimSpace(os.Getenv("ZEA_SERVICE_ACCOUNT_NAME")),
		HTTPTimeout:             20 * time.Second,
		ShutdownTimeout:         10 * time.Second,
		StreamPollInterval:      10 * time.Second,
		StreamStepTimeout:       6 * time.Hour,
		StreamRunHistory:        30,
	}

	skip, err := parseBool("ZEA_INSECURE_SKIP_PROXY_AUTH", false)
	if err != nil {
		return nil, err
	}
	cfg.InsecureSkipProxyAuth = skip

	if err := cfg.LogLevel.UnmarshalText([]byte(getEnv("ZEA_LOG_LEVEL", "info"))); err != nil {
		return nil, fmt.Errorf("invalid ZEA_LOG_LEVEL: %w", err)
	}

	if v := getEnv("ZEA_HTTP_TIMEOUT", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid ZEA_HTTP_TIMEOUT %q: expected a positive duration such as 20s", v)
		}
		cfg.HTTPTimeout = d
	}

	if err := parseDuration("ZEA_STREAM_POLL_INTERVAL", 2*time.Second, time.Hour, &cfg.StreamPollInterval); err != nil {
		return nil, err
	}
	if err := parseDuration("ZEA_STREAM_STEP_TIMEOUT", time.Minute, 72*time.Hour, &cfg.StreamStepTimeout); err != nil {
		return nil, err
	}
	if v := getEnv("ZEA_STREAM_RUN_HISTORY", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return nil, fmt.Errorf("invalid ZEA_STREAM_RUN_HISTORY %q: expected a number between 1 and 500", v)
		}
		cfg.StreamRunHistory = n
	}

	if _, _, ok := strings.Cut(cfg.AnchorApp, ":"); cfg.AnchorApp != "" && !ok {
		return nil, fmt.Errorf("invalid ZEA_ANCHOR_APP %q: expected <namespace>:<application>", cfg.AnchorApp)
	}

	if cfg.ProxyToken == "" && !cfg.InsecureSkipProxyAuth {
		return nil, errors.New("ZEA_PROXY_TOKEN must be set (or ZEA_INSECURE_SKIP_PROXY_AUTH=true for local development)")
	}
	if cfg.AnchorApp == "" {
		return nil, errors.New("ZEA_ANCHOR_APP must be set to <namespace>:<application> of the anchor Application")
	}
	if cfg.ConnectionsNamespace == "" {
		return nil, errors.New("ZEA_CONNECTIONS_NAMESPACE must be set")
	}

	return cfg, nil
}

// SplitList splits a comma-separated list, trimming blanks.
func SplitList(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// parseDuration reads an optional duration bounded by [lo, hi].
func parseDuration(key string, lo, hi time.Duration, dst *time.Duration) error {
	v := getEnv(key, "")
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return fmt.Errorf("invalid %s %q: expected a duration between %s and %s", key, v, lo, hi)
	}
	*dst = d
	return nil
}

func parseBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return b, nil
}
