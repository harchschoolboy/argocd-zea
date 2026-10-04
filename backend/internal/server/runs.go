package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// Limits for run parameters accepted from the UI.
const (
	maxRunParams     = 100
	maxParamKeyLen   = 255
	maxParamValueLen = 10 << 10
)

func (a *api) handleRunForm(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	form, err := p.GetRunForm(r.Context(), c, r.PathValue("pipeline"), ref)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, form)
}

type triggerInput struct {
	PipelineID string            `json:"pipelineID"`
	Ref        string            `json:"ref"`
	Inputs     map[string]string `json:"inputs"`
	Variables  map[string]string `json:"variables"`
}

func checkParams(kind string, m map[string]string) error {
	if len(m) > maxRunParams {
		return providers.Invalidf("at most %d %s are allowed", maxRunParams, kind)
	}
	for k, v := range m {
		if strings.TrimSpace(k) == "" || len(k) > maxParamKeyLen {
			return providers.Invalidf("%s name %q is not valid", kind, k)
		}
		if len(v) > maxParamValueLen {
			return providers.Invalidf("value of %q is too long", k)
		}
	}
	return nil
}

func (a *api) handleTrigger(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	var in triggerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.PipelineID = strings.TrimSpace(in.PipelineID)
	in.Ref = strings.TrimSpace(in.Ref)
	if in.PipelineID == "" || in.Ref == "" {
		writeError(w, http.StatusBadRequest, "pipelineID and ref are required")
		return
	}
	if err := checkParams("inputs", in.Inputs); err != nil {
		a.writeErr(w, err)
		return
	}
	if err := checkParams("variables", in.Variables); err != nil {
		a.writeErr(w, err)
		return
	}
	user := IdentityFrom(r.Context()).Username
	run, err := p.Trigger(r.Context(), c, providers.TriggerRequest{
		PipelineID: in.PipelineID,
		Ref:        in.Ref,
		Inputs:     in.Inputs,
		Variables:  in.Variables,
	})
	if err != nil {
		a.log.Warn("pipeline trigger failed", "connection", c.Name, "pipeline", in.PipelineID, "ref", in.Ref, "user", user, "error", err)
		a.writeErr(w, err)
		return
	}
	// Values may be sensitive; only names are logged.
	a.log.Info("pipeline triggered", "connection", c.Name, "pipeline", in.PipelineID, "ref", in.Ref,
		"inputs", sortedKeys(in.Inputs), "variables", sortedKeys(in.Variables), "run", run.ID, "user", user)
	writeJSON(w, http.StatusCreated, run)
}

func (a *api) handleListRuns(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := providers.RunFilter{
		PipelineID: strings.TrimSpace(q.Get("pipeline")),
		Ref:        strings.TrimSpace(q.Get("ref")),
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be a number")
			return
		}
		f.Limit = n
	}
	runs, err := p.ListRuns(r.Context(), c, f)
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) handleGetRun(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	run, err := p.GetRun(r.Context(), c, r.PathValue("run"))
	if err != nil {
		a.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (a *api) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("run")
	if err := p.CancelRun(r.Context(), c, runID); err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("run canceled", "connection", c.Name, "run", runID, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusAccepted)
}

type retryInput struct {
	FailedOnly bool `json:"failedOnly"`
}

func (a *api) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	c, p, ok := a.usableProvider(w, r)
	if !ok {
		return
	}
	var in retryInput
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	runID := r.PathValue("run")
	if err := p.RetryRun(r.Context(), c, runID, in.FailedOnly); err != nil {
		a.writeErr(w, err)
		return
	}
	a.log.Info("run retried", "connection", c.Name, "run", runID, "failedOnly", in.FailedOnly, "user", IdentityFrom(r.Context()).Username)
	w.WriteHeader(http.StatusAccepted)
}

func sortedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}
