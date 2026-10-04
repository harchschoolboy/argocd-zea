package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// maxVariables caps free pipeline variables per run.
const maxVariables = 50

var variableKeyRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

func checkPipelineID(id string) error {
	if id != projectPipelineID {
		return providers.Invalidf("unknown pipeline %q; GitLab projects have a single pipeline %q", id, projectPipelineID)
	}
	return nil
}

// GetRunForm reads spec:inputs from the CI configuration header at ref.
// Free variables are always accepted.
func (p *Provider) GetRunForm(ctx context.Context, c *connections.Connection, pipelineID, ref string) (*providers.RunForm, error) {
	if err := checkPipelineID(pipelineID); err != nil {
		return nil, err
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	pj, err := p.project(ctx, c, t)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = pj.DefaultBranch
	}
	ciPath := pj.CIConfigPath
	if ciPath == "" {
		ciPath = defaultCIPath
	}
	form := &providers.RunForm{Inputs: []providers.Input{}, Variables: true}
	if strings.Contains(ciPath, "@") || strings.Contains(ciPath, "://") {
		form.Warning = "CI configuration is outside the repository; declared inputs are not shown"
		return form, nil
	}
	var raw []byte
	_, err = p.do(ctx, c, providers.Request{
		URL:     t.projectURL("/repository/files/%s/raw?ref=%s", url.PathEscape(ciPath), url.QueryEscape(ref)),
		RawBody: &raw,
	})
	if providers.IsStatus(err, http.StatusNotFound) {
		return nil, providers.Invalidf("%s does not exist on %s", ciPath, ref)
	}
	if err != nil {
		return nil, err
	}
	inputs, err := specInputs(raw)
	if err != nil {
		form.Warning = fmt.Sprintf("cannot read spec:inputs from %s: %v", ciPath, err)
		return form, nil
	}
	form.Inputs = inputs
	return form, nil
}

// specInputs parses the optional "spec: inputs:" header document of a
// GitLab CI configuration. Inputs without a default are required.
func specInputs(data []byte) ([]providers.Input, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return []providers.Input{}, nil
		}
		return nil, err
	}
	out := []providers.Input{}
	if len(doc.Content) == 0 {
		return out, nil
	}
	inputs := mapValue(mapValue(doc.Content[0], "spec"), "inputs")
	if inputs == nil || inputs.Kind != yaml.MappingNode {
		return out, nil
	}
	for i := 0; i+1 < len(inputs.Content); i += 2 {
		def := inputs.Content[i+1]
		in := providers.Input{Name: inputs.Content[i].Value, Type: providers.InputString, Required: true}
		if t := scalar(mapValue(def, "type")); t != "" {
			in.Type = t
		}
		in.Description = scalar(mapValue(def, "description"))
		if d := mapValue(def, "default"); d != nil {
			in.Required = false
			in.Default = nodeString(d)
		}
		if opts := mapValue(def, "options"); opts != nil && opts.Kind == yaml.SequenceNode {
			for _, o := range opts.Content {
				in.Options = append(in.Options, scalar(o))
			}
			if in.Type == providers.InputString {
				in.Type = providers.InputChoice
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// nodeString renders scalars as is and collections as JSON.
func nodeString(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return scalar(n)
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			for v.Kind == yaml.AliasNode && v.Alias != nil {
				v = v.Alias
			}
			return v
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

type pipelineJSON struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid"`
	Name      string    `json:"name"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	WebURL    string    `json:"web_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      *struct {
		Username string `json:"username"`
	} `json:"user"`
}

func (pl *pipelineJSON) toRun() providers.Run {
	r := providers.Run{
		ID:         strconv.FormatInt(pl.ID, 10),
		Number:     pl.IID,
		PipelineID: projectPipelineID,
		Name:       "Pipeline",
		Title:      pl.Name,
		Ref:        pl.Ref,
		CommitSHA:  pl.SHA,
		Event:      pl.Source,
		Status:     status(pl.Status),
		WebURL:     pl.WebURL,
		CreatedAt:  pl.CreatedAt,
		UpdatedAt:  pl.UpdatedAt,
	}
	if pl.User != nil {
		r.Actor = pl.User.Username
	}
	return r
}

type variableJSON struct {
	Key          string `json:"key"`
	Value        string `json:"value"`
	VariableType string `json:"variable_type"`
}

// Trigger creates a pipeline at ref with free variables and typed inputs.
func (p *Provider) Trigger(ctx context.Context, c *connections.Connection, req providers.TriggerRequest) (*providers.Run, error) {
	if err := checkPipelineID(req.PipelineID); err != nil {
		return nil, err
	}
	if req.Ref == "" {
		return nil, providers.Invalidf("ref is required")
	}
	if len(req.Variables) > maxVariables {
		return nil, providers.Invalidf("at most %d variables are allowed", maxVariables)
	}
	body := map[string]any{"ref": req.Ref}
	if len(req.Variables) > 0 {
		vars := make([]variableJSON, 0, len(req.Variables))
		for k, v := range req.Variables {
			if !variableKeyRe.MatchString(k) {
				return nil, providers.Invalidf("variable name %q may contain only letters, digits and _", k)
			}
			vars = append(vars, variableJSON{Key: k, Value: v, VariableType: "env_var"})
		}
		body["variables"] = vars
	}
	if len(req.Inputs) > 0 {
		form, err := p.GetRunForm(ctx, c, req.PipelineID, req.Ref)
		if err != nil {
			return nil, err
		}
		inputs, err := typedInputs(form.Inputs, req.Inputs)
		if err != nil {
			return nil, err
		}
		body["inputs"] = inputs
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	var pl pipelineJSON
	if _, err := p.do(ctx, c, providers.Request{Method: http.MethodPost, URL: t.projectURL("/pipeline"), Body: body, Out: &pl}); err != nil {
		return nil, err
	}
	run := pl.toRun()
	return &run, nil
}

// typedInputs converts form values to the JSON types GitLab expects.
func typedInputs(declared []providers.Input, values map[string]string) (map[string]any, error) {
	types := map[string]string{}
	for _, in := range declared {
		types[in.Name] = in.Type
	}
	out := map[string]any{}
	for k, v := range values {
		typ, ok := types[k]
		if !ok {
			return nil, providers.Invalidf("input %q is not declared in spec:inputs", k)
		}
		switch typ {
		case providers.InputBoolean:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, providers.Invalidf("input %q must be true or false", k)
			}
			out[k] = b
		case providers.InputNumber:
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, providers.Invalidf("input %q must be a number", k)
			}
			out[k] = n
		case providers.InputArray:
			var arr []any
			if err := json.Unmarshal([]byte(v), &arr); err != nil {
				return nil, providers.Invalidf("input %q must be a JSON array", k)
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}
	return out, nil
}

// ListRuns lists recent pipelines of the project.
func (p *Provider) ListRuns(ctx context.Context, c *connections.Connection, f providers.RunFilter) ([]providers.Run, error) {
	if f.PipelineID != "" {
		if err := checkPipelineID(f.PipelineID); err != nil {
			return nil, err
		}
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	u := t.projectURL("/pipelines?per_page=%d", providers.ClampLimit(f.Limit))
	if f.Ref != "" {
		u += "&ref=" + url.QueryEscape(f.Ref)
	}
	var items []pipelineJSON
	if _, err := p.do(ctx, c, providers.Request{URL: u, Out: &items}); err != nil {
		return nil, err
	}
	out := make([]providers.Run, 0, len(items))
	for i := range items {
		out = append(out, items[i].toRun())
	}
	return out, nil
}

type jobJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Stage      string     `json:"stage"`
	Status     string     `json:"status"`
	WebURL     string     `json:"web_url"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// GetRun returns a pipeline with its jobs (latest attempts only).
func (p *Provider) GetRun(ctx context.Context, c *connections.Connection, runID string) (*providers.RunDetail, error) {
	if err := providers.ValidateID("pipeline", runID); err != nil {
		return nil, err
	}
	t, err := resolve(c)
	if err != nil {
		return nil, err
	}
	var pl pipelineJSON
	if _, err := p.do(ctx, c, providers.Request{URL: t.projectURL("/pipelines/%s", runID), Out: &pl}); err != nil {
		return nil, err
	}
	var jobs []jobJSON
	if _, err := p.do(ctx, c, providers.Request{URL: t.projectURL("/pipelines/%s/jobs?per_page=%d", runID, perPage), Out: &jobs}); err != nil {
		return nil, err
	}
	out := &providers.RunDetail{Run: pl.toRun(), Jobs: make([]providers.Job, 0, len(jobs))}
	// GitLab lists jobs newest first; show them in pipeline order.
	for i := len(jobs) - 1; i >= 0; i-- {
		j := jobs[i]
		job := providers.Job{
			ID:     strconv.FormatInt(j.ID, 10),
			Name:   j.Name,
			Stage:  j.Stage,
			Status: status(j.Status),
			WebURL: j.WebURL,
		}
		if j.StartedAt != nil {
			job.StartedAt = *j.StartedAt
		}
		if j.FinishedAt != nil {
			job.FinishedAt = *j.FinishedAt
		}
		out.Jobs = append(out.Jobs, job)
	}
	return out, nil
}

// CancelRun cancels all running jobs of a pipeline.
func (p *Provider) CancelRun(ctx context.Context, c *connections.Connection, runID string) error {
	if err := providers.ValidateID("pipeline", runID); err != nil {
		return err
	}
	t, err := resolve(c)
	if err != nil {
		return err
	}
	_, err = p.do(ctx, c, providers.Request{Method: http.MethodPost, URL: t.projectURL("/pipelines/%s/cancel", runID)})
	return err
}

// RetryRun retries failed and canceled jobs. GitLab cannot rerun a whole
// pipeline in place; start a new one with Trigger instead.
func (p *Provider) RetryRun(ctx context.Context, c *connections.Connection, runID string, failedOnly bool) error {
	if !failedOnly {
		return fmt.Errorf("%w: GitLab retries only failed and canceled jobs", providers.ErrUnsupported)
	}
	if err := providers.ValidateID("pipeline", runID); err != nil {
		return err
	}
	t, err := resolve(c)
	if err != nil {
		return err
	}
	_, err = p.do(ctx, c, providers.Request{Method: http.MethodPost, URL: t.projectURL("/pipelines/%s/retry", runID)})
	return err
}

// status maps GitLab pipeline and job statuses to normalized ones.
func status(s string) providers.Status {
	switch s {
	case "created", "waiting_for_resource", "preparing", "pending", "scheduled", "waiting_for_callback":
		return providers.StatusQueued
	case "running":
		return providers.StatusRunning
	case "success":
		return providers.StatusSuccess
	case "failed":
		return providers.StatusFailed
	case "canceled", "canceling":
		return providers.StatusCanceled
	case "skipped":
		return providers.StatusSkipped
	case "manual":
		return providers.StatusManual
	}
	return providers.StatusUnknown
}
