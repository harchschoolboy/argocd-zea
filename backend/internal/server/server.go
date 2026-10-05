package server

import (
	"log/slog"
	"net/http"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/config"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/images"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

// Version is overridden at build time via -ldflags.
var Version = "dev"

// Deps are the collaborators of the HTTP API.
type Deps struct {
	Store         connections.Store
	Providers     *providers.Registry
	Authz         *authz.Authorizer
	Registries    registries.Store
	RegistryKinds *registries.Kinds
	Images        *images.Resolver
}

type api struct {
	cfg  *config.Config
	log  *slog.Logger
	deps Deps
}

// New builds the HTTP handler for the Zea backend.
//
// Argo CD strips the "/extensions/zea" prefix, so a UI call to
// "/extensions/zea/api/v1/me" arrives here as "/api/v1/me".
func New(cfg *config.Config, log *slog.Logger, deps Deps) http.Handler {
	a := &api{cfg: cfg, log: log, deps: deps}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/me", a.handleMe)
	mux.HandleFunc("GET /api/v1/version", handleVersion)
	mux.HandleFunc("GET /api/v1/providers", a.handleProviders)
	mux.HandleFunc("GET /api/v1/connections", a.handleListConnections)
	mux.HandleFunc("POST /api/v1/connections", a.adminOnly(a.handleCreateConnection))
	mux.HandleFunc("PUT /api/v1/connections/{name}", a.adminOnly(a.handleUpdateConnection))
	mux.HandleFunc("DELETE /api/v1/connections/{name}", a.adminOnly(a.handleDeleteConnection))
	mux.HandleFunc("POST /api/v1/connections/{name}/test", a.handleTestConnection)
	mux.HandleFunc("POST /api/v1/test-connection", a.adminOnly(a.handleTestDraft))
	mux.HandleFunc("GET /api/v1/connections/{name}/branches", a.handleBranches)
	mux.HandleFunc("GET /api/v1/connections/{name}/pipelines", a.handlePipelines)
	mux.HandleFunc("GET /api/v1/connections/{name}/pipelines/{pipeline}/form", a.handleRunForm)
	mux.HandleFunc("GET /api/v1/connections/{name}/runs", a.handleListRuns)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs", a.handleTrigger)
	mux.HandleFunc("GET /api/v1/connections/{name}/runs/{run}", a.handleGetRun)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs/{run}/cancel", a.handleCancelRun)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs/{run}/retry", a.handleRetryRun)
	mux.HandleFunc("GET /api/v1/connections/{name}/images", a.handleConnectionImages)
	mux.HandleFunc("POST /api/v1/images/preview", a.adminOnly(a.handleImagesPreview))
	mux.HandleFunc("GET /api/v1/registry-kinds", a.adminOnly(a.handleRegistryKinds))
	mux.HandleFunc("GET /api/v1/registries", a.adminOnly(a.handleListRegistries))
	mux.HandleFunc("POST /api/v1/registries", a.adminOnly(a.handleCreateRegistry))
	mux.HandleFunc("PUT /api/v1/registries/{name}", a.adminOnly(a.handleUpdateRegistry))
	mux.HandleFunc("DELETE /api/v1/registries/{name}", a.adminOnly(a.handleDeleteRegistry))
	mux.HandleFunc("POST /api/v1/registries/{name}/test", a.adminOnly(a.handleTestRegistry))
	mux.HandleFunc("POST /api/v1/test-registry", a.adminOnly(a.handleTestRegistryDraft))

	protected := requireProxyToken(cfg.ProxyToken, cfg.InsecureSkipProxyAuth, log,
		requireIdentity(requireAnchor(cfg.AnchorApp, mux)))

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", handleHealth)
	root.HandleFunc("GET /readyz", handleHealth)
	root.Handle("/api/", protected)

	return recoverPanics(log, logRequests(log, root))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version})
}

type meResponse struct {
	*argocd.Identity
	IsAdmin bool   `json:"isAdmin"`
	Version string `json:"version"`
}

func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	id := IdentityFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{Identity: id, IsAdmin: a.deps.Authz.IsAdmin(id), Version: Version})
}

func (a *api) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": a.deps.Providers.Infos()})
}

func (a *api) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.deps.Authz.IsAdmin(IdentityFrom(r.Context())) {
			writeError(w, http.StatusForbidden, "only Zea admins can manage connections and registries")
			return
		}
		next(w, r)
	}
}
