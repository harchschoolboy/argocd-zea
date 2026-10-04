package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 64 << 10

// connectionView is the client-facing shape of a Connection. Credential
// values are never returned; admins only see which keys are set.
type connectionView struct {
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	URL            string   `json:"url"`
	APIURL         string   `json:"apiURL,omitempty"`
	AllowedGroups  []string `json:"allowedGroups"`
	Editable       bool     `json:"editable"`
	CredentialKeys []string `json:"credentialKeys,omitempty"`
}

// connectionInput is the body of create/update/test-connection requests.
type connectionInput struct {
	Name          string            `json:"name"`
	Provider      string            `json:"provider"`
	URL           string            `json:"url"`
	APIURL        string            `json:"apiURL"`
	AllowedGroups []string          `json:"allowedGroups"`
	Credentials   map[string]string `json:"credentials"`
}

func (in *connectionInput) toConnection() *connections.Connection {
	groups := []string{}
	for _, g := range in.AllowedGroups {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	creds := map[string]string{}
	for k, v := range in.Credentials {
		creds[strings.TrimSpace(k)] = v
	}
	return &connections.Connection{
		Name:          strings.TrimSpace(in.Name),
		Provider:      strings.TrimSpace(in.Provider),
		URL:           strings.TrimSpace(in.URL),
		APIURL:        strings.TrimSpace(in.APIURL),
		AllowedGroups: groups,
		Credentials:   creds,
	}
}

func (a *api) view(r *http.Request, c *connections.Connection) connectionView {
	v := connectionView{
		Name:          c.Name,
		Provider:      c.Provider,
		URL:           c.URL,
		APIURL:        c.APIURL,
		AllowedGroups: c.AllowedGroups,
		Editable:      c.Editable,
	}
	if a.deps.Authz.IsAdmin(IdentityFrom(r.Context())) {
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
	id := IdentityFrom(r.Context())
	out := []connectionView{}
	for _, c := range all {
		if a.deps.Authz.CanUse(id, c) {
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
	if err := a.validate(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

func (a *api) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
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
	if err := a.validate(&merged); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	if err := a.deps.Store.Delete(r.Context(), name); err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("connection deleted", "connection", name, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestDraft tests unsaved form values. When the draft names an
// existing editable Connection, blank credentials fall back to stored ones.
func (a *api) handleTestDraft(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c := in.toConnection()
	if cur, err := a.deps.Store.Get(r.Context(), c.Name); err == nil && cur.Editable {
		c.Credentials = connections.MergeCredentials(cur.Credentials, c.Credentials)
	}
	if err := a.validate(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.runTest(w, r, c)
}

func (a *api) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	c, ok := a.usableConnection(w, r)
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
	c, p, ok := a.usableProvider(w, r)
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
	c, p, ok := a.usableProvider(w, r)
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
// use it. Inaccessible Connections are reported as not found.
func (a *api) usableConnection(w http.ResponseWriter, r *http.Request) (*connections.Connection, bool) {
	c, err := a.deps.Store.Get(r.Context(), r.PathValue("name"))
	if err == nil && !a.deps.Authz.CanUse(IdentityFrom(r.Context()), c) {
		err = connections.ErrNotFound
	}
	if err != nil {
		a.writeErr(w, err)
		return nil, false
	}
	return c, true
}

func (a *api) usableProvider(w http.ResponseWriter, r *http.Request) (*connections.Connection, providers.Provider, bool) {
	c, ok := a.usableConnection(w, r)
	if !ok {
		return nil, nil, false
	}
	p, err := a.deps.Providers.Get(c.Provider)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("connection %q: %v", c.Name, err))
		return nil, nil, false
	}
	return c, p, true
}

func (a *api) validate(c *connections.Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	p, err := a.deps.Providers.Get(c.Provider)
	if err != nil {
		return err
	}
	return p.Validate(c)
}

// writeErr maps domain and upstream errors to HTTP responses.
func (a *api) writeErr(w http.ResponseWriter, err error) {
	var ue *providers.UpstreamError
	var urlErr *url.Error
	switch {
	case errors.Is(err, connections.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, connections.ErrAlreadyExists), errors.Is(err, connections.ErrReadOnly):
		writeError(w, http.StatusConflict, err.Error())
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
