package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/authz"
	"github.com/harchschoolboy/argocd-zea/backend/internal/streams"
)

// runStepView is a step's progress with its definition's key fields, so a
// run list can be shown without the Spec snapshot.
type runStepView struct {
	streams.StepState
	Name       string `json:"name,omitempty"`
	Stage      int    `json:"stage"`
	Connection string `json:"connection"`
	Pipeline   string `json:"pipeline"`
}

// runView is the client-facing shape of a Stream run.
type runView struct {
	ID              string            `json:"id"`
	Stream          string            `json:"stream"`
	User            string            `json:"user"`
	Params          map[string]string `json:"params"`
	Status          string            `json:"status"`
	Message         string            `json:"message,omitempty"`
	CancelRequested bool              `json:"cancelRequested,omitempty"`
	CancelledBy     string            `json:"cancelledBy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	FinishedAt      time.Time         `json:"finishedAt,omitzero"`
	Steps           []runStepView     `json:"steps"`
	// Attempt is the number of the current attempt, starting at 1.
	Attempt   int       `json:"attempt"`
	RetriedBy string    `json:"retriedBy,omitempty"`
	RetriedAt time.Time `json:"retriedAt,omitzero"`
	// Attempts are the earlier attempts; only sent for a single run.
	Attempts []streams.Attempt `json:"attempts,omitempty"`
	// Tries are the automatically retried step tries; only sent for a
	// single run.
	Tries []streams.StepTry `json:"tries,omitempty"`
	// Spec is the snapshot the run executes; only sent for a single run.
	Spec *streams.Spec `json:"spec,omitempty"`
}

func runViewOf(r *streams.Run, withSpec bool) runView {
	v := runView{
		ID:              r.ID,
		Stream:          r.Stream,
		User:            r.User,
		Params:          r.Params,
		Status:          r.Status,
		Message:         r.Message,
		CancelRequested: r.CancelRequested,
		CancelledBy:     r.CancelledBy,
		CreatedAt:       r.CreatedAt,
		FinishedAt:      r.FinishedAt,
		Steps:           []runStepView{},
		Attempt:         len(r.Attempts) + 1,
		RetriedBy:       r.RetriedBy,
		RetriedAt:       r.RetriedAt,
	}
	if v.Params == nil {
		v.Params = map[string]string{}
	}
	type stepInfo struct {
		stage int
		def   streams.Step
	}
	info := map[string]stepInfo{}
	for i, stage := range r.Spec.Stages {
		for _, step := range stage.Steps {
			info[step.ID] = stepInfo{stage: i, def: step}
		}
	}
	for _, s := range r.Steps {
		in := info[s.ID]
		v.Steps = append(v.Steps, runStepView{StepState: s, Name: in.def.Name, Stage: in.stage, Connection: in.def.Connection, Pipeline: in.def.Pipeline})
	}
	if withSpec {
		spec := r.Spec
		v.Spec = &spec
		v.Attempts = r.Attempts
		v.Tries = r.Tries
	}
	return v
}

// runsReady answers 501 when the backend runs without a Stream engine.
func (a *api) runsReady(w http.ResponseWriter) bool {
	if a.deps.StreamRuns == nil {
		writeError(w, http.StatusNotImplemented, "stream runs are not configured")
		return false
	}
	return true
}

// visibleRun loads a run if the user may perform action on its Stream. Runs
// of Streams the user may not see are reported as not found.
func (a *api) visibleRun(w http.ResponseWriter, r *http.Request, action string) (*streams.Run, bool) {
	if !a.runsReady(w) {
		return nil, false
	}
	if !AccessFrom(r.Context()).Can(authz.ResourceStreams, authz.ActionView, r.PathValue("name")) {
		a.writeErr(w, streams.ErrRunNotFound)
		return nil, false
	}
	if !a.allow(w, r, authz.ResourceStreams, action, r.PathValue("name")) {
		return nil, false
	}
	run, err := a.deps.StreamRuns.Runs().Get(r.Context(), r.PathValue("name"), r.PathValue("run"))
	if err != nil {
		a.writeErr(w, err)
		return nil, false
	}
	return run, true
}

// startRunInput carries the param values; missing ones use defaults.
type startRunInput struct {
	Params map[string]string `json:"params"`
}

func (a *api) handleStartStreamRun(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w) {
		return
	}
	st, conns, ok := a.visibleStream(w, r, authz.ActionRun)
	if !ok {
		return
	}
	var in startRunInput
	if r.ContentLength != 0 && !decodeJSONMax(w, r, &in, maxStreamBodyBytes) {
		return
	}
	if problems := streamProblems(st, conns); len(problems) > 0 {
		msgs := make([]string, 0, len(problems))
		for _, p := range problems {
			msgs = append(msgs, p.String())
		}
		writeError(w, http.StatusConflict, "the stream cannot run: "+strings.Join(msgs, "; "))
		return
	}
	run, err := a.deps.StreamRuns.Start(r.Context(), st, in.Params, IdentityFrom(r.Context()).Username)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runViewOf(run, true))
}

func (a *api) handleListStreamRuns(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w) {
		return
	}
	if !AccessFrom(r.Context()).Can(authz.ResourceStreams, authz.ActionView, r.PathValue("name")) {
		a.writeErr(w, streams.ErrNotFound)
		return
	}
	runs, err := a.deps.StreamRuns.Runs().List(r.Context(), r.PathValue("name"))
	if err != nil {
		a.writeErr(w, err)
		return
	}
	out := []runView{}
	for _, run := range runs {
		out = append(out, runViewOf(run, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (a *api) handleGetStreamRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.visibleRun(w, r, authz.ActionView)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, runViewOf(run, true))
}

func (a *api) handleCancelStreamRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.visibleRun(w, r, authz.ActionRun)
	if !ok {
		return
	}
	updated, err := a.deps.StreamRuns.Cancel(r.Context(), run.Stream, run.ID, IdentityFrom(r.Context()).Username)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, runViewOf(updated, false))
}

func (a *api) handleRetryStreamRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.visibleRun(w, r, authz.ActionRun)
	if !ok {
		return
	}
	updated, err := a.deps.StreamRuns.Retry(r.Context(), run.Stream, run.ID, IdentityFrom(r.Context()).Username)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runViewOf(updated, true))
}

// handleStreamBranches lists the branches of a Connection the Stream uses,
// for users who may run the Stream without access to the Connection.
func (a *api) handleStreamBranches(w http.ResponseWriter, r *http.Request) {
	st, conns, ok := a.visibleStream(w, r, authz.ActionRun)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("connection"))
	c := conns[name]
	if c == nil || !slices.Contains(st.Connections(), name) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("the stream does not use connection %q", name))
		return
	}
	p, err := a.providerOf(w, c)
	if err != nil {
		return
	}
	branches, err := p.ListBranches(r.Context(), c)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

// stepRan reports whether the provider run was started for the step in any
// try or attempt of the run.
func stepRan(run *streams.Run, step, providerRun string) bool {
	match := func(s streams.StepState) bool { return s.ID == step && s.RunID == providerRun }
	for _, s := range run.Steps {
		if match(s) {
			return true
		}
	}
	for _, t := range run.Tries {
		if match(t.StepState) {
			return true
		}
	}
	for _, at := range run.Attempts {
		for _, s := range at.Steps {
			if match(s) {
				return true
			}
		}
	}
	return false
}

// handleStreamRunJobs returns the provider run (with jobs) of a step, for
// users who may see the Stream without access to the Connection.
func (a *api) handleStreamRunJobs(w http.ResponseWriter, r *http.Request) {
	run, ok := a.visibleRun(w, r, authz.ActionView)
	if !ok {
		return
	}
	step := strings.TrimSpace(r.URL.Query().Get("step"))
	providerRun := r.PathValue("providerRun")
	var conn string
	for _, stage := range run.Spec.Stages {
		for _, s := range stage.Steps {
			if s.ID == step {
				conn = s.Connection
			}
		}
	}
	if conn == "" || !stepRan(run, step, providerRun) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("step %q of the run did not start pipeline run %q", step, providerRun))
		return
	}
	c, err := a.deps.Store.Get(r.Context(), conn)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	p, err := a.providerOf(w, c)
	if err != nil {
		return
	}
	detail, err := p.GetRun(r.Context(), c, providerRun)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
