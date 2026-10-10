// Command zea-backend serves the Zea Argo CD proxy extension API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/config"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/images"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers/github"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers/gitlab"
	"github.com/harchschoolboy/argocd-zea/backend/internal/proxytoken"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
	"github.com/harchschoolboy/argocd-zea/backend/internal/server"
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// imagesCacheTTL is how long registry listings are reused.
const imagesCacheTTL = time.Minute

func main() {
	cmd := run
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init-proxy-token":
			cmd = runInitProxyToken
		default:
			fmt.Fprintf(os.Stderr, "usage: %s [init-proxy-token]\n", os.Args[0])
			os.Exit(2)
		}
	}
	if err := cmd(); err != nil {
		slog.Error("zea-backend failed", "error", err)
		os.Exit(1)
	}
}

// runInitProxyToken creates the proxy token Secret if it is missing. Used by
// the Helm chart's pre-install/pre-upgrade hook (an Argo CD PreSync hook).
func runInitProxyToken() error {
	ns, name, key := os.Getenv("ZEA_PROXY_SECRET_NAMESPACE"), os.Getenv("ZEA_PROXY_SECRET_NAME"), os.Getenv("ZEA_PROXY_SECRET_KEY")
	if ns == "" || name == "" || key == "" {
		return errors.New("ZEA_PROXY_SECRET_NAMESPACE, ZEA_PROXY_SECRET_NAME and ZEA_PROXY_SECRET_KEY are required")
	}
	kube, err := kubeClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := proxytoken.Ensure(ctx, kube, ns, name, key)
	if err != nil {
		return err
	}
	slog.Info("proxy token secret ready", "namespace", ns, "name", name, "key", key, "result", res)
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	if cfg.InsecureSkipProxyAuth {
		log.Warn("proxy token check is DISABLED; never run like this in a cluster")
	}

	kube, err := kubeClient()
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout, Transport: http.DefaultTransport.(*http.Transport).Clone()}
	regStore := registries.NewSecretStore(kube, cfg.ConnectionsNamespace)
	gar := registries.NewGAR(httpClient)
	if cfg.ServiceAccountNamespace != "" && cfg.ServiceAccountName != "" {
		gar.SetSubjectTokens(registries.NewKubeServiceAccountTokens(kube, cfg.ServiceAccountNamespace, cfg.ServiceAccountName))
	}
	regKinds := registries.NewKinds(registries.NewDigitalOcean(httpClient), gar, registries.NewOCI(httpClient))
	for _, ref := range cfg.RegistryPullSecrets {
		if _, _, err := registries.ParsePullSecretRef(ref); err != nil {
			log.Warn("ignoring registry pull secret", "error", err)
		}
	}
	regKinds.SetPullSecrets(registries.NewKubePullSecrets(kube, cfg.RegistryPullSecrets))
	connStore := connections.NewSecretStore(kube, cfg.ConnectionsNamespace)
	provs := providers.NewRegistry(github.New(httpClient), gitlab.New(httpClient))
	engine := streams.NewEngine(streams.NewConfigMapRunStore(kube, cfg.ConnectionsNamespace), connStore, provs, streams.EngineConfig{
		PollInterval: cfg.StreamPollInterval,
		StepTimeout:  cfg.StreamStepTimeout,
		History:      cfg.StreamRunHistory,
	}, log)
	policyStore := authz.NewConfigMapPolicyStore(kube, cfg.ConnectionsNamespace)
	deps := server.Deps{
		Store:         connStore,
		Providers:     provs,
		Authz:         authz.New(cfg.AdminUsers, cfg.AdminGroups, policyStore),
		Registries:    regStore,
		RegistryKinds: regKinds,
		Images:        images.NewResolver(regStore, regKinds, imagesCacheTTL),
		Streams:       streams.NewConfigMapStore(kube, cfg.ConnectionsNamespace),
		StreamRuns:    engine,
	}
	if len(cfg.AdminUsers) == 0 && len(cfg.AdminGroups) == 0 {
		log.Warn("no Zea admins configured (ZEA_ADMIN_USERS / ZEA_ADMIN_GROUPS); the access policy can only be managed declaratively")
	}
	migrateAccess(log, deps, policyStore)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.New(cfg, log, deps),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	engineDone := make(chan struct{})
	go func() {
		engine.Run(ctx)
		close(engineDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("zea-backend listening", "addr", cfg.ListenAddr, "version", server.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	select {
	case <-engineDone:
	case <-shutdownCtx.Done():
	}
	return err
}

// migrateAccess creates the access policy from the deprecated allowedGroups
// of Connections on the first start of a version with policies. Failures
// are logged: without a policy only admins have access, and the migration
// is tried again on the next start.
func migrateAccess(log *slog.Logger, deps server.Deps, store authz.PolicyStore) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	created, err := server.MigrateLegacyAccess(ctx, deps, store)
	switch {
	case err != nil:
		log.Error("migrating allowedGroups into the access policy failed", "error", err)
		return
	case created:
		log.Info("access policy created from connection allowedGroups", "configmap", authz.ConfigMapName)
		return
	}
	conns, err := deps.Store.List(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if len(c.AllowedGroups) > 0 {
			log.Warn("allowedGroups is deprecated and ignored; grant access in the access policy instead", "connection", c.Name)
		}
	}
}

// kubeClient uses the in-cluster service account, falling back to the
// standard kubeconfig loading rules (KUBECONFIG, ~/.kube/config) for local runs.
func kubeClient() (kubernetes.Interface, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config and no usable kubeconfig: %w", err)
		}
	}
	rc.UserAgent = "argocd-zea/" + server.Version
	return kubernetes.NewForConfig(rc)
}
