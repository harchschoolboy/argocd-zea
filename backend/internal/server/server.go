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
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
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
	// Streams stores Zea Streams; nil disables the streams API.
	Streams streams.Store
	// StreamRuns executes Streams; nil disables the stream runs API.
	StreamRuns *streams.Engine
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
	mux.HandleFunc("GET /api/v1/policy", a.adminOnly(a.handleGetPolicy))
	mux.HandleFunc("PUT /api/v1/policy", a.adminOnly(a.handleSavePolicy))
	mux.HandleFunc("POST /api/v1/policy/validate", a.adminOnly(a.handleValidatePolicy))
	mux.HandleFunc("POST /api/v1/policy/evaluate", a.adminOnly(a.handleEvaluatePolicy))
	mux.HandleFunc("GET /api/v1/connections", a.handleListConnections)
	mux.HandleFunc("POST /api/v1/connections", requireAny(authz.ResourceConnections, authz.ActionEdit, a.handleCreateConnection))
	mux.HandleFunc("PUT /api/v1/connections/{name}", a.handleUpdateConnection)
	mux.HandleFunc("DELETE /api/v1/connections/{name}", a.handleDeleteConnection)
	mux.HandleFunc("POST /api/v1/connections/{name}/test", a.handleTestConnection)
	mux.HandleFunc("POST /api/v1/test-connection", requireAny(authz.ResourceConnections, authz.ActionEdit, a.handleTestDraft))
	mux.HandleFunc("GET /api/v1/connections/{name}/branches", a.handleBranches)
	mux.HandleFunc("GET /api/v1/connections/{name}/pipelines", a.handlePipelines)
	mux.HandleFunc("GET /api/v1/connections/{name}/pipelines/{pipeline}/form", a.handleRunForm)
	mux.HandleFunc("GET /api/v1/connections/{name}/runs", a.handleListRuns)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs", a.handleTrigger)
	mux.HandleFunc("GET /api/v1/connections/{name}/runs/{run}", a.handleGetRun)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs/{run}/cancel", a.handleCancelRun)
	mux.HandleFunc("POST /api/v1/connections/{name}/runs/{run}/retry", a.handleRetryRun)
	mux.HandleFunc("GET /api/v1/connections/{name}/images", a.handleConnectionImages)
	mux.HandleFunc("POST /api/v1/images/preview", requireAny(authz.ResourceConnections, authz.ActionEdit, a.handleImagesPreview))
	mux.HandleFunc("GET /api/v1/registry-kinds", a.handleRegistryKinds)
	mux.HandleFunc("GET /api/v1/registries", a.handleListRegistries)
	mux.HandleFunc("POST /api/v1/registries", requireAny(authz.ResourceRegistries, authz.ActionEdit, a.handleCreateRegistry))
	mux.HandleFunc("PUT /api/v1/registries/{name}", a.handleUpdateRegistry)
	mux.HandleFunc("DELETE /api/v1/registries/{name}", a.handleDeleteRegistry)
	mux.HandleFunc("POST /api/v1/registries/{name}/test", a.handleTestRegistry)
	mux.HandleFunc("POST /api/v1/test-registry", requireAny(authz.ResourceRegistries, authz.ActionEdit, a.handleTestRegistryDraft))
	mux.HandleFunc("GET /api/v1/streams", a.handleListStreams)
	mux.HandleFunc("POST /api/v1/streams", requireAny(authz.ResourceStreams, authz.ActionEdit, a.handleCreateStream))
	mux.HandleFunc("POST /api/v1/streams/import", requireAny(authz.ResourceStreams, authz.ActionEdit, a.handleImportStream))
	mux.HandleFunc("POST /api/v1/streams/validate", requireAny(authz.ResourceStreams, authz.ActionEdit, a.handleValidateStream))
	mux.HandleFunc("GET /api/v1/streams/{name}", a.handleGetStream)
	mux.HandleFunc("PUT /api/v1/streams/{name}", a.handleUpdateStream)
	mux.HandleFunc("DELETE /api/v1/streams/{name}", a.handleDeleteStream)
	mux.HandleFunc("POST /api/v1/streams/{name}/draft", requireAny(authz.ResourceStreams, authz.ActionEdit, a.handleDraftStream))
	mux.HandleFunc("GET /api/v1/streams/{name}/export", a.handleExportStream)
	mux.HandleFunc("GET /api/v1/streams/{name}/branches", a.handleStreamBranches)
	mux.HandleFunc("GET /api/v1/streams/{name}/runs", a.handleListStreamRuns)
	mux.HandleFunc("POST /api/v1/streams/{name}/runs", a.handleStartStreamRun)
	mux.HandleFunc("GET /api/v1/streams/{name}/runs/{run}", a.handleGetStreamRun)
	mux.HandleFunc("POST /api/v1/streams/{name}/runs/{run}/cancel", a.handleCancelStreamRun)
	mux.HandleFunc("POST /api/v1/streams/{name}/runs/{run}/retry", a.handleRetryStreamRun)
	mux.HandleFunc("GET /api/v1/streams/{name}/runs/{run}/jobs/{providerRun}", a.handleStreamRunJobs)

	protected := requireProxyToken(cfg.ProxyToken, cfg.InsecureSkipProxyAuth, log,
		requireIdentity(requireAnchor(cfg.AnchorApp, a.withAccess(mux))))

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
	// Permissions maps each resource type to the actions the user may
	// perform on at least some of its items.
	Permissions map[string][]string `json:"permissions"`
}

func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	acc := AccessFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{
		Identity:    IdentityFrom(r.Context()),
		IsAdmin:     acc.IsAdmin(),
		Version:     Version,
		Permissions: acc.Summary(),
	})
}

func (a *api) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": a.deps.Providers.Infos()})
}
