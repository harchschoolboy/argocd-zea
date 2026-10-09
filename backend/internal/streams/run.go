package streams

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Step statuses within a Run.
const (
	// StepPending waits for its dependencies.
	StepPending = "pending"
	// StepStarting was triggered, but its pipeline run is not known yet.
	StepStarting  = "starting"
	StepRunning   = "running"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepCancelled = "cancelled"
	// StepSkipped did not run because of its condition, a cancelled Run,
	// or because the provider skipped the pipeline.
	StepSkipped = "skipped"
)

var (
	// ErrRunNotFound is returned when a Stream run does not exist.
	ErrRunNotFound = errors.New("stream run not found")
	// ErrRunFinished is returned when cancelling a finished run.
	ErrRunFinished = errors.New("stream run has already finished")
	// ErrRunConflict is returned when a run changed since it was read.
	ErrRunConflict = errors.New("stream run was changed concurrently; try again")
	// ErrInvalidParams is returned for missing or invalid run params.
	ErrInvalidParams = errors.New("invalid stream params")
	// ErrNotRunnable is returned when a Stream has problems.
	ErrNotRunnable = errors.New("stream cannot run")
	// ErrNotRetryable is returned when a run cannot be retried.
	ErrNotRetryable = errors.New("stream run cannot be retried")
)

// MaxAttempts limits how often one run may be retried.
const MaxAttempts = 20

var runIDRe = regexp.MustCompile(`^[0-9a-z]{1,13}-[0-9a-z]{1,10}$`)

// Run is one execution of a Stream. It keeps a snapshot of the Spec, so
// editing the Stream does not change runs in progress.
type Run struct {
	ID              string            `json:"id"`
	Stream          string            `json:"stream"`
	User            string            `json:"user"`
	Params          map[string]string `json:"params"`
	Spec            Spec              `json:"spec"`
	Status          string            `json:"status"`
	Message         string            `json:"message,omitempty"`
	CancelRequested bool              `json:"cancelRequested,omitempty"`
	CancelledBy     string            `json:"cancelledBy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	FinishedAt      time.Time         `json:"finishedAt,omitzero"`
	Steps           []StepState       `json:"steps"`
	// Attempts are the earlier, retried attempts of the run, oldest first.
	Attempts  []Attempt `json:"attempts,omitempty"`
	RetriedBy string    `json:"retriedBy,omitempty"`
	RetriedAt time.Time `json:"retriedAt,omitzero"`
	// Tries are the failed tries of steps that were retried automatically,
	// oldest first.
	Tries []StepTry `json:"tries,omitempty"`
	// ResourceVersion guards updates against concurrent writers.
	ResourceVersion string `json:"-"`
}

// StepState is the progress of one step of a Run.
type StepState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Ref is the rendered branch or tag the pipeline was started at.
	Ref       string `json:"ref,omitempty"`
	RunID     string `json:"runId,omitempty"`
	RunNumber int64  `json:"runNumber,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	// ProviderStatus is the provider's normalized run status.
	ProviderStatus string    `json:"providerStatus,omitempty"`
	TriggeredAt    time.Time `json:"triggeredAt,omitzero"`
	FinishedAt     time.Time `json:"finishedAt,omitzero"`
	Message        string    `json:"message,omitempty"`
	// Try counts the automatic retries of the step in the current attempt.
	Try int `json:"try,omitempty"`
	// RetryAt delays the start of a pending step that is retried.
	RetryAt time.Time `json:"retryAt,omitzero"`
}

// StepTry is a failed try of a step that was retried automatically.
type StepTry struct {
	// Attempt is the run attempt the try belongs to.
	Attempt int `json:"attempt"`
	StepState
}

// Attempt is a finished attempt of a run that was retried afterwards.
type Attempt struct {
	Number     int       `json:"number"`
	User       string    `json:"user"`
	Status     string    `json:"status"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	// Steps are the step states the retry replaced; steps that succeeded
	// are kept by the retry and are not listed.
	Steps []StepState `json:"steps"`
}

// StepFinished reports whether a step status is final.
func StepFinished(status string) bool {
	switch status {
	case StepSucceeded, StepFailed, StepCancelled, StepSkipped:
		return true
	}
	return false
}

// ValidRunID reports whether id looks like an id made by NewRun.
func ValidRunID(id string) bool { return runIDRe.MatchString(id) }

// NewRun prepares a Run of st. The caller must have checked the Stream's
// Connections; NewRun checks the Stream itself and the params.
func NewRun(st *Stream, given map[string]string, user string, now time.Time) (*Run, error) {
	if problems := st.Problems(); len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotRunnable, problems[0])
	}
	params, err := ResolveParams(st.Params, given)
	if err != nil {
		return nil, err
	}
	id, err := newRunID(now)
	if err != nil {
		return nil, err
	}
	r := &Run{
		ID:        id,
		Stream:    st.Name,
		User:      user,
		Params:    params,
		Spec:      clone(&Stream{Spec: st.Spec}).Spec,
		Status:    RunRunning,
		CreatedAt: now.UTC(),
	}
	for _, stage := range st.Stages {
		for _, step := range stage.Steps {
			r.Steps = append(r.Steps, StepState{ID: step.ID, Status: StepPending})
		}
	}
	return r, nil
}

// ResolveParams applies defaults to the given values and checks them
// against the param definitions.
func ResolveParams(defs []Param, given map[string]string) (map[string]string, error) {
	known := make(map[string]bool, len(defs))
	for _, p := range defs {
		known[p.Name] = true
	}
	for _, k := range sortedKeys(given) {
		if !known[k] {
			return nil, fmt.Errorf("%w: unknown param %q", ErrInvalidParams, k)
		}
	}
	out := make(map[string]string, len(defs))
	for _, p := range defs {
		v := given[p.Name]
		if strings.TrimSpace(v) == "" {
			v = p.Default
		}
		if len(v) > maxValueLen {
			return nil, fmt.Errorf("%w: param %q is too long", ErrInvalidParams, p.Name)
		}
		switch p.Type {
		case ParamBoolean:
			if v == "" {
				v = "false"
			}
			if v != "true" && v != "false" {
				return nil, fmt.Errorf("%w: param %q must be true or false", ErrInvalidParams, p.Name)
			}
		case ParamChoice:
			if v != "" && !contains(p.Options, v) {
				return nil, fmt.Errorf("%w: param %q must be one of %s", ErrInvalidParams, p.Name, strings.Join(p.Options, ", "))
			}
		case ParamBranch:
			v = strings.TrimSpace(v)
		}
		if p.Required && strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%w: param %q is required", ErrInvalidParams, p.Name)
		}
		out[p.Name] = v
	}
	return out, nil
}

// newRunID returns a sortable id that is safe in ConfigMap names.
func newRunID(now time.Time) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(36*36*36*36*36))
	if err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	suffix := strconv.FormatInt(n.Int64(), 36)
	return strconv.FormatInt(now.UnixMilli(), 36) + "-" + strings.Repeat("0", 5-len(suffix)) + suffix, nil
}

// Connections returns the Connections the run's snapshot uses.
func (r *Run) Connections() []string {
	return (&Stream{Spec: r.Spec}).Connections()
}

// Graph resolves the snapshot's dependencies.
func (r *Run) Graph() Graph {
	return (&Stream{Spec: r.Spec}).Graph()
}

// StepDefs maps step ids to their definitions in the snapshot.
func (r *Run) StepDefs() map[string]Step {
	out := map[string]Step{}
	for _, stage := range r.Spec.Stages {
		for _, step := range stage.Steps {
			out[step.ID] = step
		}
	}
	return out
}

// values are the ${{ ... }} values available to the run's steps.
func (r *Run) values() map[string]string {
	v := map[string]string{"zea.user": r.User, "stream.name": r.Stream, "stream.run": r.ID}
	for k, p := range r.Params {
		v["params."+k] = p
	}
	for _, s := range r.Steps {
		p := "steps." + s.ID + "."
		v[p+"status"] = s.Status
		v[p+"runId"] = s.RunID
		v[p+"url"] = s.URL
		v[p+"sha"] = s.SHA
		v[p+"ref"] = s.Ref
	}
	return v
}

// failedHard reports a step failure that dependants and the Run must not
// ignore.
func failedHard(s StepState, def Step) bool {
	return (s.Status == StepFailed || s.Status == StepCancelled) && !def.ContinueOnError
}

// retry resets the steps that did not succeed so the Engine runs them
// again; succeeded steps and their values are kept. The run's snapshot is
// reused, so later edits of the Stream do not apply.
func (r *Run) retry(user string, now time.Time) error {
	if r.Status == RunRunning {
		return fmt.Errorf("%w: it is still running", ErrNotRetryable)
	}
	if len(r.Attempts)+1 >= MaxAttempts {
		return fmt.Errorf("%w: it was retried %d times; start a new run", ErrNotRetryable, len(r.Attempts))
	}
	var replaced []StepState
	for i := range r.Steps {
		s := &r.Steps[i]
		if s.Status == StepSucceeded {
			continue
		}
		replaced = append(replaced, *s)
		*s = StepState{ID: s.ID, Status: StepPending}
	}
	if len(replaced) == 0 {
		return fmt.Errorf("%w: every step succeeded", ErrNotRetryable)
	}
	started, by := r.CreatedAt, r.User
	if n := len(r.Attempts); n > 0 {
		started, by = r.RetriedAt, r.RetriedBy
	}
	r.Attempts = append(r.Attempts, Attempt{
		Number:     len(r.Attempts) + 1,
		User:       by,
		Status:     r.Status,
		Message:    r.Message,
		StartedAt:  started,
		FinishedAt: r.FinishedAt,
		Steps:      replaced,
	})
	r.Status, r.Message, r.FinishedAt = RunRunning, "", time.Time{}
	r.CancelRequested, r.CancelledBy = false, ""
	r.RetriedBy, r.RetriedAt = user, now.UTC()
	return nil
}
