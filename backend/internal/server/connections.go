package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
	"github.com/harchschoolboy/argocd-zea/backend/internal/registries"
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 64 << 10

// connectionView is the client-facing shape of a Connection. Credential
// values are never returned; admins only see which keys are set.
type connectionView struct {
	Name           string                    `json:"name"`
	Provider       string                    `json:"provider"`
	URL            string                    `json:"url"`
	APIURL         string                    `json:"apiURL,omitempty"`
	Images         []connections.ImageSource `json:"images"`
	ImagesError    string                    `json:"imagesError,omitempty"`
	Editable       bool                      `json:"editable"`
	CredentialKeys []string                  `json:"credentialKeys,omitempty"`
	// Actions are what the user may do with the Connection.
	Actions []string `json:"actions"`
}

// connectionInput is the body of create/update/test-connection requests.
type connectionInput struct {
	Name        string                    `json:"name"`
	Provider    string                    `json:"provider"`
	URL         string                    `json:"url"`
	APIURL      string                    `json:"apiURL"`
	Images      []connections.ImageSource `json:"images"`
	Credentials map[string]string         `json:"credentials"`
}

func (in *connectionInput) toConnection() *connections.Connection {
	creds := map[string]string{}
	for k, v := range in.Credentials {
		creds[strings.TrimSpace(k)] = v
	}
	imgs := []connections.ImageSource{}
	for _, s := range in.Images {
		imgs = append(imgs, connections.ImageSource{
			Registry:   strings.TrimSpace(s.Registry),
			Repository: strings.TrimSpace(s.Repository),
			Tags:       strings.TrimSpace(s.Tags),
		})
	}
	return &connections.Connection{
		Name:        strings.TrimSpace(in.Name),
		Provider:    strings.TrimSpace(in.Provider),
		URL:         strings.TrimSpace(in.URL),
		APIURL:      strings.TrimSpace(in.APIURL),
		Images:      imgs,
		Credentials: creds,
	}
}

func (a *api) view(r *http.Request, c *connections.Connection) connectionView {
	acc := AccessFrom(r.Context())
	v := connectionView{
		Name:        c.Name,
		Provider:    c.Provider,
		URL:         c.URL,
		APIURL:      c.APIURL,
		Images:      c.Images,
		ImagesError: c.ImagesError,
		Editable:    c.Editable,
		Actions:     acc.Actions(authz.ResourceConnections, c.Name),
	}
	if v.Images == nil {
		v.Images = []connections.ImageSource{}
	}
	if acc.Can(authz.ResourceConnections, authz.ActionEdit, c.Name) {
		v.CredentialKeys = c.CredentialKeys()
	}
	return v
}

func (a *api) handleListConnections(w http.ResponseWriter, r *http.Request) {
	all, err := a.deps.Store.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	acc := AccessFrom(r.Context())
	out := []connectionView{}
	for _, c := range all {
		if acc.Can(authz.ResourceConnections, authz.ActionView, c.Name) {
			out = append(out, a.view(r, c))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

func (a *api) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c := in.toConnection()
	if !allowNew(w, r, authz.ResourceConnections, authz.ActionEdit, c.Name) {
		return
	}
	if err := a.validate(r.Context(), c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.allowRegistries(w, r, c.Images, nil) {
		return
	}
	created, err := a.deps.Store.Create(r.Context(), c)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("connection created", "connection", c.Name, "provider", c.Provider, "user", IdentityFrom(r.Context()).Username)
	writeJSON(w, http.StatusCreated, a.view(r, created))
}

// allowRegistries requires view access to every registry the image sources
// add, so users cannot read registries they were not given.
func (a *api) allowRegistries(w http.ResponseWriter, r *http.Request, srcs, before []connections.ImageSource) bool {
	had := map[string]bool{}
	for _, s := range before {
		had[s.Registry] = true
	}
	acc := AccessFrom(r.Context())
	var denied []string
	for _, s := range srcs {
		if !had[s.Registry] && !acc.Can(authz.ResourceRegistries, authz.ActionView, s.Registry) && !slices.Contains(denied, s.Registry) {
			denied = append(denied, s.Registry)
		}
	}
	if len(denied) > 0 {
		writeError(w, http.StatusForbidden, "you may not use registries: "+strings.Join(denied, ", "))
		return false
	}
	return true
}

func (a *api) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !a.allow(w, r, authz.ResourceConnections, authz.ActionEdit, name) {
		return
	}
	var in connectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name != "" && in.Name != name {
		writeError(w, http.StatusBadRequest, "connection name cannot be changed")
		return
	}
	in.Name = name
	cur, err := a.deps.Store.Get(r.Context(), name)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	if !cur.Editable {
		a.writeErr(w, connections.ErrReadOnly)
		return
	}
	c := in.toConnection()
	merged := *c
	merged.Credentials = connections.MergeCredentials(cur.Credentials, c.Credentials)
	if err := a.validate(r.Context(), &merged); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.allowRegistries(w, r, c.Images, cur.Images) {
		return
	}
	updated, err := a.deps.Store.Update(r.Context(), c)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("connection updated", "connection", name, "user", IdentityFrom(r.Context()).Username)
	writeJSON(w, http.StatusOK, a.view(r, updated))
}

func (a *api) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !a.allow(w, r, authz.ResourceConnections, authz.ActionEdit, name) {
		return
	}
	if err := a.deps.Store.Delete(r.Context(), name); err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("connection deleted", "connection", name, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestDraft tests unsaved form values. When the draft names an
// existing editable Connection the user may edit, blank credentials fall
// back to stored ones.
func (a *api) handleTestDraft(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c := in.toConnection()
	// Stored credentials are only reused for Connections the user may
	// edit; otherwise a draft could send them to another URL.
	if AccessFrom(r.Context()).Can(authz.ResourceConnections, authz.ActionEdit, c.Name) {
		if cur, err := a.deps.Store.Get(r.Context(), c.Name); err == nil && cur.Editable {
			c.Credentials = connections.MergeCredentials(cur.Credentials, c.Credentials)
		}
	}
	if err := a.validate(r.Context(), c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.runTest(w, r, c)
}

func (a *api) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	c, ok := a.usableConnection(w, r, authz.ActionView)
	if !ok {
		return
	}
	a.runTest(w, r, c)
}

func (a *api) runTest(w http.ResponseWriter, r *http.Request, c *connections.Connection) {
	p, err := a.deps.Providers.Get(c.Provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := p.Test(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repository": repo})
}

func (a *api) handleBranches(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r, authz.ActionView)
	if !ok {
		return
	}
	branches, err := p.ListBranches(r.Context(), c)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

func (a *api) handlePipelines(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r, authz.ActionView)
	if !ok {
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	pls, err := p.ListPipelines(r.Context(), c, ref)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": pls})
}

// usableConnection loads the Connection named in the path if the user may
// perform action on it. Connections the user may not see are reported as
// not found.
func (a *api) usableConnection(w http.ResponseWriter, r *http.Request, action string) (*connections.Connection, bool) {
	name := r.PathValue("name")
	if !a.allow(w, r, authz.ResourceConnections, action, name) {
		return nil, false
	}
	c, err := a.deps.Store.Get(r.Context(), name)
	if err != nil {
		a.writeErr(w, err)
		return nil, false
	}
	return c, true
}

func (a *api) usableProvider(w http.ResponseWriter, r *http.Request, action string) (*connections.Connection, providers.Provider, bool) {
	c, ok := a.usableConnection(w, r, action)
	if !ok {
		return nil, nil, false
	}
	p, err := a.providerOf(w, c)
	return c, p, err == nil
}

// providerOf answers 409 when the Connection's provider is unknown.
func (a *api) providerOf(w http.ResponseWriter, c *connections.Connection) (providers.Provider, error) {
	p, err := a.deps.Providers.Get(c.Provider)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("connection %q: %v", c.Name, err))
	}
	return p, err
}

func (a *api) validate(ctx context.Context, c *connections.Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	p, err := a.deps.Providers.Get(c.Provider)
	if err != nil {
		return err
	}
	if err := p.Validate(c); err != nil {
		return err
	}
	return a.validateImageRegistries(ctx, c.Images)
}

// validateImageRegistries checks that every image source names a registry.
func (a *api) validateImageRegistries(ctx context.Context, srcs []connections.ImageSource) error {
	for i, s := range srcs {
		if a.deps.Registries == nil {
			return errors.New("registries are not configured")
		}
		if _, err := a.deps.Registries.Get(ctx, s.Registry); err != nil {
			if errors.Is(err, registries.ErrNotFound) {
				return fmt.Errorf("image source %d: registry %q does not exist", i+1, s.Registry)
			}
			return err
		}
	}
	return nil
}

// writeErr maps domain and upstream errors to HTTP responses.
func (a *api) writeErr(w http.ResponseWriter, err error) {
	var ue *providers.UpstreamError
	var urlErr *url.Error
	if status, ok := isPolicyErr(err); ok {
		writeError(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, connections.ErrNotFound), errors.Is(err, registries.ErrNotFound), errors.Is(err, streams.ErrNotFound),
		errors.Is(err, streams.ErrRunNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, connections.ErrAlreadyExists), errors.Is(err, connections.ErrReadOnly),
		errors.Is(err, registries.ErrAlreadyExists), errors.Is(err, registries.ErrReadOnly),
		errors.Is(err, streams.ErrAlreadyExists), errors.Is(err, streams.ErrReadOnly), errors.Is(err, streams.ErrConflict),
		errors.Is(err, streams.ErrRunFinished), errors.Is(err, streams.ErrNotRetryable), errors.Is(err, streams.ErrRunConflict), errors.Is(err, streams.ErrNotRunnable):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, providers.ErrInvalidRequest), errors.Is(err, providers.ErrUnsupported), errors.Is(err, streams.ErrInvalidParams):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &ue) && (ue.Status == http.StatusBadRequest || ue.Status == http.StatusUnprocessableEntity):
		// The provider rejected user input (unknown workflow input, bad ref).
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.As(err, &ue):
		writeError(w, http.StatusBadGateway, err.Error())
	case errors.As(err, &urlErr):
		writeError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "upstream request timed out")
	default:
		a.log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONMax(w, r, v, maxBodyBytes)
}

func decodeJSONMax(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
