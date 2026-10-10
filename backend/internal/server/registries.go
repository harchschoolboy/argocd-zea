package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/images"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
)

// registryView is the admin-facing shape of a registry. Credential values
// are never returned.
type registryView struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	URL            string   `json:"url"`
	Editable       bool     `json:"editable"`
	CredentialKeys []string `json:"credentialKeys"`
	// UsedBy lists the Connections whose image sources use the registry,
	// limited to those the user may see.
	UsedBy []string `json:"usedBy"`
	// Actions are what the user may do with the registry.
	Actions []string `json:"actions"`
}

// registryInput is the body of create/update/test-registry requests.
type registryInput struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	URL         string            `json:"url"`
	Credentials map[string]string `json:"credentials"`
}

func (in *registryInput) toRegistry() *registries.Registry {
	creds := map[string]string{}
	for k, v := range in.Credentials {
		creds[strings.TrimSpace(k)] = v
	}
	return &registries.Registry{
		Name:        strings.TrimSpace(in.Name),
		Kind:        strings.TrimSpace(in.Kind),
		URL:         strings.TrimSpace(in.URL),
		Credentials: creds,
	}
}

func registryViewOf(acc *authz.Access, r *registries.Registry, usedBy []string) registryView {
	visible := []string{}
	for _, c := range usedBy {
		if acc.Can(authz.ResourceConnections, authz.ActionView, c) {
			visible = append(visible, c)
		}
	}
	v := registryView{
		Name:           r.Name,
		Kind:           r.Kind,
		URL:            r.URL,
		Editable:       r.Editable,
		CredentialKeys: []string{},
		UsedBy:         visible,
		Actions:        acc.Actions(authz.ResourceRegistries, r.Name),
	}
	if acc.Can(authz.ResourceRegistries, authz.ActionEdit, r.Name) {
		v.CredentialKeys = r.CredentialKeys()
	}
	return v
}

// registryUsage maps registry names to the Connections referencing them.
func (a *api) registryUsage(ctx context.Context) (map[string][]string, error) {
	conns, err := a.deps.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, c := range conns {
		seen := map[string]bool{}
		for _, s := range c.Images {
			if !seen[s.Registry] {
				seen[s.Registry] = true
				out[s.Registry] = append(out[s.Registry], c.Name)
			}
		}
	}
	return out, nil
}

// registriesReady answers 501 when the backend runs without registries.
func (a *api) registriesReady(w http.ResponseWriter) bool {
	if a.deps.Registries == nil || a.deps.RegistryKinds == nil || a.deps.Images == nil {
		writeError(w, http.StatusNotImplemented, "registries are not configured")
		return false
	}
	return true
}

func (a *api) handleRegistryKinds(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	if !AccessFrom(r.Context()).CanAny(authz.ResourceRegistries, authz.ActionView) {
		writeError(w, http.StatusForbidden, "you may not view registries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": a.deps.RegistryKinds.Infos()})
}

func (a *api) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	all, err := a.deps.Registries.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	usage, err := a.registryUsage(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	acc := AccessFrom(r.Context())
	out := []registryView{}
	for _, reg := range all {
		if acc.Can(authz.ResourceRegistries, authz.ActionView, reg.Name) {
			out = append(out, registryViewOf(acc, reg, usage[reg.Name]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"registries": out})
}

func (a *api) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	var in registryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	reg := in.toRegistry()
	if !allowNew(w, r, authz.ResourceRegistries, authz.ActionEdit, reg.Name) {
		return
	}
	if err := a.deps.RegistryKinds.Validate(reg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := a.deps.Registries.Create(r.Context(), reg)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("registry created", "registry", reg.Name, "kind", reg.Kind, "user", IdentityFrom(r.Context()).Username)
	writeJSON(w, http.StatusCreated, registryViewOf(AccessFrom(r.Context()), created, nil))
}

func (a *api) handleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	name := r.PathValue("name")
	if !a.allow(w, r, authz.ResourceRegistries, authz.ActionEdit, name) {
		return
	}
	var in registryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name != "" && in.Name != name {
		writeError(w, http.StatusBadRequest, "registry name cannot be changed")
		return
	}
	in.Name = name
	cur, err := a.deps.Registries.Get(r.Context(), name)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	if !cur.Editable {
		a.writeErr(w, registries.ErrReadOnly)
		return
	}
	reg := in.toRegistry()
	merged := *reg
	merged.Credentials = connections.MergeCredentials(cur.Credentials, reg.Credentials)
	if err := a.deps.RegistryKinds.Validate(&merged); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := a.deps.Registries.Update(r.Context(), reg)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.deps.Images.Invalidate(name)
	usage, err := a.registryUsage(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("registry updated", "registry", name, "user", IdentityFrom(r.Context()).Username)
	writeJSON(w, http.StatusOK, registryViewOf(AccessFrom(r.Context()), updated, usage[name]))
}

func (a *api) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	name := r.PathValue("name")
	if !a.allow(w, r, authz.ResourceRegistries, authz.ActionEdit, name) {
		return
	}
	usage, err := a.registryUsage(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	if used := usage[name]; len(used) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("registry %q is used by connections: %s", name, strings.Join(used, ", ")))
		return
	}
	if err := a.deps.Registries.Delete(r.Context(), name); err != nil {
		a.writeErr(w, err)
		return
	}
	a.deps.Images.Invalidate(name)
	a.log.Info("registry deleted", "registry", name, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleTestRegistry(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	if !a.allow(w, r, authz.ResourceRegistries, authz.ActionEdit, r.PathValue("name")) {
		return
	}
	reg, err := a.deps.Registries.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.runRegistryTest(w, r, reg)
}

// handleTestRegistryDraft tests unsaved form values. Blank credentials of an
// existing editable registry the user may edit fall back to the stored ones.
func (a *api) handleTestRegistryDraft(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	var in registryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	reg := in.toRegistry()
	if AccessFrom(r.Context()).Can(authz.ResourceRegistries, authz.ActionEdit, reg.Name) {
		if cur, err := a.deps.Registries.Get(r.Context(), reg.Name); err == nil && cur.Editable {
			reg.Credentials = connections.MergeCredentials(cur.Credentials, reg.Credentials)
		}
	}
	if err := a.deps.RegistryKinds.Validate(reg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.runRegistryTest(w, r, reg)
}

// maxTestSample caps the repository names returned by a registry test.
const maxTestSample = 20

func (a *api) runRegistryTest(w http.ResponseWriter, r *http.Request, reg *registries.Registry) {
	c, err := a.deps.RegistryKinds.Get(reg.Kind)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repos, err := c.ListRepositories(r.Context(), reg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	sort.Strings(names)
	if len(names) > maxTestSample {
		names = names[:maxTestSample]
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repositoryCount": len(repos), "repositories": names})
}

// previewInput is the body of an image sources preview.
type previewInput struct {
	Images []connections.ImageSource `json:"images"`
	Ref    string                    `json:"ref"`
}

// previewTags is the number of tags per repository in a preview.
const previewTags = 3

func (a *api) handleImagesPreview(w http.ResponseWriter, r *http.Request) {
	if !a.registriesReady(w) {
		return
	}
	var in previewInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := connections.ValidateImageSources(in.Images); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.allowRegistries(w, r, in.Images, nil) {
		return
	}
	res := a.deps.Images.Resolve(r.Context(), in.Images, images.Options{Ref: strings.TrimSpace(in.Ref), TagLimit: previewTags})
	writeJSON(w, http.StatusOK, res)
}

// imagesResponse is the body of GET /connections/{name}/images.
type imagesResponse struct {
	// Configured is false when the Connection has no image sources.
	Configured bool `json:"configured"`
	*images.Result
}

func (a *api) handleConnectionImages(w http.ResponseWriter, r *http.Request) {
	c, ok := a.usableConnection(w, r, authz.ActionView)
	if !ok {
		return
	}
	if c.ImagesError != "" {
		writeJSON(w, http.StatusOK, imagesResponse{Configured: true, Result: &images.Result{
			Repositories: []images.Repository{},
			Errors:       []images.SourceError{{Error: "images: " + c.ImagesError}},
		}})
		return
	}
	if len(c.Images) == 0 {
		writeJSON(w, http.StatusOK, imagesResponse{Result: &images.Result{Repositories: []images.Repository{}}})
		return
	}
	if !a.registriesReady(w) {
		return
	}
	q := r.URL.Query()
	opts := images.Options{
		Ref:     strings.TrimSpace(q.Get("ref")),
		Refresh: q.Get("refresh") == "1" || q.Get("refresh") == "true",
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be a number")
			return
		}
		opts.TagLimit = n
	}
	if p, err := a.deps.Providers.Get(c.Provider); err == nil {
		if cl, ok := p.(providers.CommitLinker); ok {
			opts.CommitURL = func(sha string) string { return cl.CommitURL(c, sha) }
		}
	}
	writeJSON(w, http.StatusOK, imagesResponse{Configured: true, Result: a.deps.Images.Resolve(r.Context(), c.Images, opts)})
}
