package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// maxDispatchInputs is the GitHub limit of workflow_dispatch inputs.
const maxDispatchInputs = 25

// send performs a mutating call with a token that may write Actions.
func (p *Provider) send(ctx context.Context, c *connections.Connection, t *target, rawURL string, body, out any) error {
	tok, err := p.token(ctx, c, t, writePermissions)
	if err != nil {
		return err
	}
	_, err = providers.Do(ctx, p.client, providerName, providers.Request{
		Method: http.MethodPost,
		URL:    rawURL,
		Header: apiHeaders(tok),
		Body:   body,
		Out:    out,
	})
	return err
}

// GetRunForm reads workflow_dispatch inputs of a workflow at ref.
func (p *Provider) GetRunForm(ctx context.Context, c *connections.Connection, pipelineID, ref string) (*providers.RunForm, error) {
	if err := providers.ValidateID("workflow", pipelineID); err != nil {
		return nil, err
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		repo, err := p.repository(ctx, c, t)
		if err != nil {
			return nil, err
		}
		ref = repo.DefaultBranch
	}
	var w workflowJSON
	if _, err := p.get(ctx, c, t, t.repoURL("/actions/workflows/%s", pipelineID), &w); err != nil {
		return nil, err
	}
	raw, err := p.workflowFile(ctx, c, t, w.Path, ref)
	if providers.IsStatus(err, http.StatusNotFound) {
		return nil, providers.Invalidf("workflow file %s does not exist on %s", w.Path, ref)
	}
	if err != nil {
		return nil, err
	}
	ok, inputs, err := parseDispatch(raw)
	if err != nil {
		return nil, providers.Invalidf("cannot parse workflow file: %v", err)
	}
	if !ok {
		return nil, providers.Invalidf("workflow has no workflow_dispatch trigger on %s", ref)
	}
	if inputs == nil {
		inputs = []providers.Input{}
	}
	return &providers.RunForm{Inputs: inputs}, nil
}

type dispatchResponse struct {
	WorkflowRunID int64  `json:"workflow_run_id"`
	HTMLURL       string `json:"html_url"`
}

// Trigger creates a workflow_dispatch event. GitHub validates inputs against
// the workflow definition at ref and rejects unknown keys.
func (p *Provider) Trigger(ctx context.Context, c *connections.Connection, req providers.TriggerRequest) (*providers.Run, error) {
	if err := providers.ValidateID("workflow", req.PipelineID); err != nil {
		return nil, err
	}
	if req.Ref == "" {
		return nil, providers.Invalidf("ref is required")
	}
	if len(req.Variables) > 0 {
		return nil, providers.Invalidf("GitHub workflows accept only declared inputs, not free variables")
	}
	if len(req.Inputs) > maxDispatchInputs {
		return nil, providers.Invalidf("GitHub accepts at most %d inputs", maxDispatchInputs)
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	inputs := req.Inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	dispatchURL := t.repoURL("/actions/workflows/%s/dispatches", req.PipelineID)
	body := map[string]any{"ref": req.Ref, "inputs": inputs, "return_run_details": true}
	var resp dispatchResponse
	err = p.send(ctx, c, t, dispatchURL, body, &resp)
	if err != nil && providers.IsStatus(err, http.StatusUnprocessableEntity) && strings.Contains(err.Error(), "return_run_details") {
		// Older GitHub Enterprise Server versions do not know the parameter.
		delete(body, "return_run_details")
		err = p.send(ctx, c, t, dispatchURL, body, &resp)
	}
	if err != nil {
		return nil, err
	}
	run := &providers.Run{PipelineID: req.PipelineID, Ref: req.Ref, Event: "workflow_dispatch", Status: providers.StatusQueued, CreatedAt: p.now()}
	if resp.WorkflowRunID != 0 {
		run.ID = strconv.FormatInt(resp.WorkflowRunID, 10)
		run.WebURL = resp.HTMLURL
	}
	return run, nil
}

type actorJSON struct {
	Login string `json:"login"`
}

type runJSON struct {
	ID              int64     `json:"id"`
	RunNumber       int64     `json:"run_number"`
	WorkflowID      int64     `json:"workflow_id"`
	Name            string    `json:"name"`
	DisplayTitle    string    `json:"display_title"`
	HeadBranch      string    `json:"head_branch"`
	HeadSHA         string    `json:"head_sha"`
	Event           string    `json:"event"`
	Status          string    `json:"status"`
	Conclusion      string    `json:"conclusion"`
	HTMLURL         string    `json:"html_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Actor           actorJSON `json:"actor"`
	TriggeringActor actorJSON `json:"triggering_actor"`
}

func (r *runJSON) toRun() providers.Run {
	actor := r.TriggeringActor.Login
	if actor == "" {
		actor = r.Actor.Login
	}
	return providers.Run{
		ID:         strconv.FormatInt(r.ID, 10),
		Number:     r.RunNumber,
		PipelineID: strconv.FormatInt(r.WorkflowID, 10),
		Name:       r.Name,
		Title:      r.DisplayTitle,
		Ref:        r.HeadBranch,
		CommitSHA:  r.HeadSHA,
		Event:      r.Event,
		Actor:      actor,
		Status:     status(r.Status, r.Conclusion),
		WebURL:     r.HTMLURL,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

// ListRuns lists recent workflow runs of the repository or of one workflow.
func (p *Provider) ListRuns(ctx context.Context, c *connections.Connection, f providers.RunFilter) ([]providers.Run, error) {
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	limit := providers.ClampLimit(f.Limit)
	var u string
	if f.PipelineID != "" {
		if err := providers.ValidateID("workflow", f.PipelineID); err != nil {
			return nil, err
		}
		u = t.repoURL("/actions/workflows/%s/runs?per_page=%d", f.PipelineID, limit)
	} else {
		u = t.repoURL("/actions/runs?per_page=%d", limit)
	}
	if f.Ref != "" {
		u += "&branch=" + url.QueryEscape(f.Ref)
	}
	var page struct {
		WorkflowRuns []runJSON `json:"workflow_runs"`
	}
	if _, err := p.get(ctx, c, t, u, &page); err != nil {
		return nil, err
	}
	out := make([]providers.Run, 0, len(page.WorkflowRuns))
	for i := range page.WorkflowRuns {
		out = append(out, page.WorkflowRuns[i].toRun())
	}
	return out, nil
}

type jobJSON struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Steps       []struct {
		Number     int    `json:"number"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"steps"`
}

// GetRun returns a workflow run with the jobs of its latest attempt.
func (p *Provider) GetRun(ctx context.Context, c *connections.Connection, runID string) (*providers.RunDetail, error) {
	if err := providers.ValidateID("run", runID); err != nil {
		return nil, err
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	var r runJSON
	if _, err := p.get(ctx, c, t, t.repoURL("/actions/runs/%s", runID), &r); err != nil {
		return nil, err
	}
	var jobs struct {
		Jobs []jobJSON `json:"jobs"`
	}
	if _, err := p.get(ctx, c, t, t.repoURL("/actions/runs/%s/jobs?per_page=%d", runID, perPage), &jobs); err != nil {
		return nil, err
	}
	out := &providers.RunDetail{Run: r.toRun(), Jobs: make([]providers.Job, 0, len(jobs.Jobs))}
	for _, j := range jobs.Jobs {
		job := providers.Job{
			ID:         strconv.FormatInt(j.ID, 10),
			Name:       j.Name,
			Status:     status(j.Status, j.Conclusion),
			WebURL:     j.HTMLURL,
			StartedAt:  derefTime(j.StartedAt),
			FinishedAt: derefTime(j.CompletedAt),
		}
		for _, s := range j.Steps {
			job.Steps = append(job.Steps, providers.Step{Number: s.Number, Name: s.Name, Status: status(s.Status, s.Conclusion)})
		}
		out.Jobs = append(out.Jobs, job)
	}
	return out, nil
}

// CancelRun cancels a queued or running workflow run.
func (p *Provider) CancelRun(ctx context.Context, c *connections.Connection, runID string) error {
	if err := providers.ValidateID("run", runID); err != nil {
		return err
	}
	t, err := resolve(c)
	if err != nil {
		return err
	}
	return p.send(ctx, c, t, t.repoURL("/actions/runs/%s/cancel", runID), nil, nil)
}

// RetryRun reruns all jobs, or only failed jobs, of a completed run.
func (p *Provider) RetryRun(ctx context.Context, c *connections.Connection, runID string, failedOnly bool) error {
	if err := providers.ValidateID("run", runID); err != nil {
		return err
	}
	t, err := resolve(c)
	if err != nil {
		return err
	}
	path := "/actions/runs/%s/rerun"
	if failedOnly {
		path = "/actions/runs/%s/rerun-failed-jobs"
	}
	return p.send(ctx, c, t, t.repoURL(path, runID), nil, nil)
}

// status maps GitHub status and conclusion to a normalized status.
func status(s, conclusion string) providers.Status {
	switch s {
	case "queued", "pending", "requested":
		return providers.StatusQueued
	case "waiting":
		// Waiting for an environment approval.
		return providers.StatusManual
	case "in_progress":
		return providers.StatusRunning
	case "completed":
		switch conclusion {
		case "success", "neutral":
			return providers.StatusSuccess
		case "failure", "timed_out", "startup_failure":
			return providers.StatusFailed
		case "cancelled":
			return providers.StatusCanceled
		case "skipped":
			return providers.StatusSkipped
		case "action_required":
			return providers.StatusManual
		}
	}
	return providers.StatusUnknown
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
