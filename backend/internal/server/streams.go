package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/argocd"
	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// maxStreamBodyBytes caps Stream request bodies (imports carry YAML).
const maxStreamBodyBytes = 1 << 20

// streamView is the client-facing shape of a Stream.
type streamView struct {
	Name    string `json:"name"`
	DraftOf string `json:"draftOf,omitempty"`
	streams.Spec
	Editable bool `json:"editable"`
	// Version must be sent back on update to detect concurrent edits.
	Version     string            `json:"version"`
	Connections []string          `json:"connections"`
	Problems    []streams.Problem `json:"problems"`
}

// streamInput is the body of create, update and validate requests.
type streamInput struct {
	Name string `json:"name"`
	streams.Spec
	Version string `json:"version"`
}

func (in *streamInput) toStream() *streams.Stream {
	return &streams.Stream{Name: strings.TrimSpace(in.Name), Spec: in.Spec, ResourceVersion: in.Version}
}

// streamsReady answers 501 when the backend runs without a Stream store.
func (a *api) streamsReady(w http.ResponseWriter) bool {
	if a.deps.Streams == nil {
		writeError(w, http.StatusNotImplemented, "streams are not configured")
		return false
	}
	return true
}

func (a *api) connectionIndex(ctx context.Context) (map[string]*connections.Connection, error) {
	all, err := a.deps.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*connections.Connection, len(all))
	for _, c := range all {
		out[c.Name] = c
	}
	return out, nil
}

// canSeeStream: admins see every Stream; other users need access to every
// Connection the Stream uses.
func (a *api) canSeeStream(id *argocd.Identity, st *streams.Stream, conns map[string]*connections.Connection) bool {
	if a.deps.Authz.IsAdmin(id) {
		return true
	}
	return a.canUseAll(id, st.Connections(), conns)
}

// canUseAll reports whether the user may use every named Connection. An
// empty list is never usable by non-admins.
func (a *api) canUseAll(id *argocd.Identity, used []string, conns map[string]*connections.Connection) bool {
	if a.deps.Authz.IsAdmin(id) {
		return true
	}
	if len(used) == 0 {
		return false
	}
	for _, name := range used {
		c, ok := conns[name]
		if !ok || !a.deps.Authz.CanUse(id, c) {
			return false
		}
	}
	return true
}

// streamProblems adds missing Connections to the Stream's own problems.
func streamProblems(st *streams.Stream, conns map[string]*connections.Connection) []streams.Problem {
	out := st.Problems()
	if st.ParseError != "" {
		return out
	}
	for _, p := range st.Params {
		if p.Type == streams.ParamBranch && p.Connection != "" && conns[p.Connection] == nil {
			out = append(out, streams.Problem{Param: p.Name, Message: fmt.Sprintf("connection %q does not exist", p.Connection)})
		}
	}
	for _, stage := range st.Stages {
		for _, step := range stage.Steps {
			if step.Connection != "" && conns[step.Connection] == nil {
				out = append(out, streams.Problem{Step: step.ID, Message: fmt.Sprintf("connection %q does not exist", step.Connection)})
			}
		}
	}
	return out
}

func streamViewOf(st *streams.Stream, conns map[string]*connections.Connection) streamView {
	v := streamView{
		Name:        st.Name,
		DraftOf:     st.DraftOf,
		Spec:        st.Spec,
		Editable:    st.Editable,
		Version:     st.ResourceVersion,
		Connections: st.Connections(),
		Problems:    streamProblems(st, conns),
	}
	if v.Params == nil {
		v.Params = []streams.Param{}
	}
	if v.Stages == nil {
		v.Stages = []streams.Stage{}
	}
	for i := range v.Stages {
		if v.Stages[i].Steps == nil {
			v.Stages[i].Steps = []streams.Step{}
		}
	}
	if v.Connections == nil {
		v.Connections = []string{}
	}
	if v.Problems == nil {
		v.Problems = []streams.Problem{}
	}
	return v
}

// visibleStream loads a Stream and hides it (404) from users who may not
// see it.
func (a *api) visibleStream(w http.ResponseWriter, r *http.Request) (*streams.Stream, map[string]*connections.Connection, bool) {
	if !a.streamsReady(w) {
		return nil, nil, false
	}
	st, err := a.deps.Streams.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		a.writeErr(w, err)
		return nil, nil, false
	}
	conns, err := a.connectionIndex(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return nil, nil, false
	}
	if !a.canSeeStream(IdentityFrom(r.Context()), st, conns) {
		a.writeErr(w, streams.ErrNotFound)
		return nil, nil, false
	}
	return st, conns, true
}

func (a *api) handleListStreams(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	all, err := a.deps.Streams.List(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	conns, err := a.connectionIndex(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	id := IdentityFrom(r.Context())
	out := []streamView{}
	for _, st := range all {
		if a.canSeeStream(id, st, conns) {
			out = append(out, streamViewOf(st, conns))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": out})
}

func (a *api) handleGetStream(w http.ResponseWriter, r *http.Request) {
	st, conns, ok := a.visibleStream(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, streamViewOf(st, conns))
}

// respondStream writes a stored Stream with its problems.
func (a *api) respondStream(w http.ResponseWriter, r *http.Request, status int, st *streams.Stream) {
	conns, err := a.connectionIndex(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, status, streamViewOf(st, conns))
}

func (a *api) handleCreateStream(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	var in streamInput
	if !decodeJSONMax(w, r, &in, maxStreamBodyBytes) {
		return
	}
	st := in.toStream()
	st.ResourceVersion = ""
	if err := st.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := a.deps.Streams.Create(r.Context(), st)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("stream created", "stream", st.Name, "user", IdentityFrom(r.Context()).Username)
	a.respondStream(w, r, http.StatusCreated, created)
}

func (a *api) handleUpdateStream(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	var in streamInput
	if !decodeJSONMax(w, r, &in, maxStreamBodyBytes) {
		return
	}
	name := r.PathValue("name")
	if in.Name != "" && in.Name != name {
		writeError(w, http.StatusBadRequest, "stream name cannot be changed")
		return
	}
	in.Name = name
	cur, err := a.deps.Streams.Get(r.Context(), name)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	if !cur.Editable {
		a.writeErr(w, streams.ErrReadOnly)
		return
	}
	st := in.toStream()
	st.DraftOf = cur.DraftOf
	if err := st.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := a.deps.Streams.Update(r.Context(), st)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("stream updated", "stream", name, "user", IdentityFrom(r.Context()).Username)
	a.respondStream(w, r, http.StatusOK, updated)
}

func (a *api) handleDeleteStream(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	name := r.PathValue("name")
	if err := a.deps.Streams.Delete(r.Context(), name); err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("stream deleted", "stream", name, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusNoContent)
}

// draftInput names the draft; empty means "<stream>-draft".
type draftInput struct {
	Name string `json:"name"`
}

// handleDraftStream copies a Stream into an editable draft. Drafts of
// drafts point to the original Stream.
func (a *api) handleDraftStream(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	var in draftInput
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	src, err := a.deps.Streams.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		a.writeErr(w, err)
		return
	}
	if src.ParseError != "" {
		writeError(w, http.StatusConflict, "the stream cannot be read: "+src.ParseError)
		return
	}
	draft := &streams.Stream{Name: strings.TrimSpace(in.Name), DraftOf: src.DraftOf, Spec: src.Spec}
	if draft.DraftOf == "" {
		draft.DraftOf = src.Name
	}
	if draft.Name == "" {
		draft.Name = src.Name + "-draft"
	}
	if err := draft.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := a.deps.Streams.Create(r.Context(), draft)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("stream draft created", "stream", src.Name, "draft", draft.Name, "user", IdentityFrom(r.Context()).Username)
	a.respondStream(w, r, http.StatusCreated, created)
}

func (a *api) handleExportStream(w http.ResponseWriter, r *http.Request) {
	st, _, ok := a.visibleStream(w, r)
	if !ok {
		return
	}
	if st.ParseError != "" {
		writeError(w, http.StatusConflict, "the stream cannot be read: "+st.ParseError)
		return
	}
	name, manifest, err := streams.Export(st, a.cfg.ConnectionsNamespace)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name":     name,
		"fileName": streams.ConfigMapNamePrefix + name + ".yaml",
		"yaml":     manifest,
	})
}

// importInput carries YAML; Replace overwrites an existing UI-managed Stream.
type importInput struct {
	YAML    string `json:"yaml"`
	Replace bool   `json:"replace"`
}

func (a *api) handleImportStream(w http.ResponseWriter, r *http.Request) {
	if !a.streamsReady(w) {
		return
	}
	var in importInput
	if !decodeJSONMax(w, r, &in, maxStreamBodyBytes) {
		return
	}
	st, err := streams.Import(in.YAML)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := st.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user := IdentityFrom(r.Context()).Username
	if in.Replace {
		cur, err := a.deps.Streams.Get(r.Context(), st.Name)
		switch {
		case err == nil:
			if !cur.Editable {
				a.writeErr(w, streams.ErrReadOnly)
				return
			}
			st.DraftOf = cur.DraftOf
			updated, err := a.deps.Streams.Update(r.Context(), st)
			if err != nil {
				a.writeErr(w, err)
				return
			}
			a.log.Info("stream imported", "stream", st.Name, "replaced", true, "user", user)
			a.respondStream(w, r, http.StatusOK, updated)
			return
		case !errors.Is(err, streams.ErrNotFound):
			a.writeErr(w, err)
			return
		}
	}
	created, err := a.deps.Streams.Create(r.Context(), st)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("stream imported", "stream", st.Name, "user", user)
	a.respondStream(w, r, http.StatusCreated, created)
}

// handleValidateStream reports the problems of an unsaved Stream.
func (a *api) handleValidateStream(w http.ResponseWriter, r *http.Request) {
	var in streamInput
	if !decodeJSONMax(w, r, &in, maxStreamBodyBytes) {
		return
	}
	st := in.toStream()
	if st.Name == "" {
		st.Name = "unnamed"
	}
	if err := st.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	conns, err := a.connectionIndex(r.Context())
	if err != nil {
		a.writeErr(w, err)
		return
	}
	problems := streamProblems(st, conns)
	if problems == nil {
		problems = []streams.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": problems})
}
