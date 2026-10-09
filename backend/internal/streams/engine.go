package streams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harchschoolboy/argocd-zea/backend/internal/connections"
	"github.com/harchschoolboy/argocd-zea/backend/internal/providers"
)

// Engine defaults.
const (
	DefaultPollInterval = 10 * time.Second
	DefaultStepTimeout  = 6 * time.Hour
	DefaultRunHistory   = 30
	// DefaultStartGrace is how long a triggered step may stay without a
	// known pipeline run before it fails.
	DefaultStartGrace = 3 * time.Minute
	// correlationSkew tolerates clock differences between Zea and the
	// provider when matching a triggered run by its creation time.
	correlationSkew = 30 * time.Second
	correlationRuns = 20
)

// ConnectionGetter loads Connections; connections.Store satisfies it.
type ConnectionGetter interface {
	Get(ctx context.Context, name string) (*connections.Connection, error)
}

// ProviderGetter resolves providers; *providers.Registry satisfies it.
type ProviderGetter interface {
	Get(id string) (providers.Provider, error)
}

// EngineConfig tunes the Engine. Zero values use the defaults.
type EngineConfig struct {
	PollInterval time.Duration
	StepTimeout  time.Duration
	History      int
	StartGrace   time.Duration
}

// Engine executes Stream runs. All state lives in the RunStore, so a
// restarted backend resumes active runs. Only one Engine may run at a time
// (the backend runs as a single replica).
type Engine struct {
	runs  RunStore
	conns ConnectionGetter
	provs ProviderGetter
	cfg   EngineConfig
	log   *slog.Logger
	now   func() time.Time
	kick  chan struct{}
	mu    sync.Mutex
}

// NewEngine returns an Engine. Call Run to start reconciling.
func NewEngine(runs RunStore, conns ConnectionGetter, provs ProviderGetter, cfg EngineConfig, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = DefaultStepTimeout
	}
	if cfg.History <= 0 {
		cfg.History = DefaultRunHistory
	}
	if cfg.StartGrace <= 0 {
		cfg.StartGrace = DefaultStartGrace
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{runs: runs, conns: conns, provs: provs, cfg: cfg, log: log, now: time.Now, kick: make(chan struct{}, 1)}
}

// Runs is the Engine's RunStore.
func (e *Engine) Runs() RunStore { return e.runs }

// Start creates a run of st. The caller has checked access and the
// Stream's Connections.
func (e *Engine) Start(ctx context.Context, st *Stream, params map[string]string, user string) (*Run, error) {
	r, err := NewRun(st, params, user, e.now())
	if err != nil {
		return nil, err
	}
	created, err := e.runs.Create(ctx, r)
	if err != nil {
		return nil, err
	}
	// Param values may be sensitive; only names are logged.
	e.log.Info("stream run started", "stream", st.Name, "run", created.ID, "user", user, "params", sortedKeys(created.Params))
	e.Kick()
	return created, nil
}

// Cancel asks the Engine to stop a run.
func (e *Engine) Cancel(ctx context.Context, stream, id, user string) (*Run, error) {
	r, err := Mutate(ctx, e.runs, stream, id, func(r *Run) error {
		if r.Status != RunRunning {
			return ErrRunFinished
		}
		r.CancelRequested = true
		if r.CancelledBy == "" {
			r.CancelledBy = user
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.log.Info("stream run cancel requested", "stream", stream, "run", id, "user", user)
	e.Kick()
	return r, nil
}

// Kick makes the Engine reconcile soon.
func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run reconciles active runs until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	e.log.Info("stream engine started", "pollInterval", e.cfg.PollInterval.String(), "stepTimeout", e.cfg.StepTimeout.String(), "history", e.cfg.History)
	t := time.NewTicker(e.cfg.PollInterval)
	defer t.Stop()
	for {
		e.ReconcileAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.kick:
		}
	}
}

// ReconcileAll advances every active run once.
func (e *Engine) ReconcileAll(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs, err := e.runs.ListActive(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Warn("could not list active stream runs", "error", err)
		}
		return
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	for _, r := range runs {
		if ctx.Err() != nil {
			return
		}
		e.reconcile(ctx, r)
	}
}

func (e *Engine) reconcile(ctx context.Context, run *Run) {
	defs := run.StepDefs()
	if run.CancelRequested {
		e.cancelRun(ctx, run, defs)
		return
	}
	for i := range run.Steps {
		switch run.Steps[i].Status {
		case StepStarting:
			e.correlate(ctx, run, i, defs[run.Steps[i].ID])
		case StepRunning:
			e.poll(ctx, run, i, defs[run.Steps[i].ID])
		}
		if ctx.Err() != nil {
			return
		}
	}
	g := run.Graph()
	for progressed := true; progressed; {
		progressed = false
		for i := range run.Steps {
			if run.Steps[i].Status != StepPending {
				continue
			}
			ready, start, reason := decide(run, run.Steps[i].ID, g, defs)
			if !ready {
				continue
			}
			if !start {
				now := e.now()
				if e.setStep(ctx, run, i, func(s *StepState) {
					s.Status, s.Message, s.FinishedAt = StepSkipped, reason, now
				}) {
					progressed = true
				}
				continue
			}
			e.start(ctx, run, i, defs[run.Steps[i].ID])
			if ctx.Err() != nil {
				return
			}
			if StepFinished(run.Steps[i].Status) {
				progressed = true
			}
		}
	}
	e.finishIfDone(ctx, run, defs)
}

// decide reports whether a pending step's dependencies have finished and,
// if so, whether its condition lets it run (or why it is skipped).
func decide(run *Run, id string, g Graph, defs map[string]Step) (ready, start bool, reason string) {
	state := make(map[string]StepState, len(run.Steps))
	for _, s := range run.Steps {
		state[s.ID] = s
	}
	deps := g[id]
	for _, d := range deps {
		if !StepFinished(state[d].Status) {
			return false, false, ""
		}
	}
	switch defs[id].When {
	case WhenAlways:
		return true, true, ""
	case WhenFailure:
		up := g.Upstream(id)
		for _, u := range sortedSet(up) {
			if failedHard(state[u], defs[u]) {
				return true, true, ""
			}
		}
		return true, false, "no earlier step failed"
	}
	for _, d := range deps {
		s := state[d]
		switch {
		case s.Status == StepSucceeded:
		case (s.Status == StepFailed || s.Status == StepCancelled) && defs[d].ContinueOnError:
		case s.Status == StepSkipped:
			return true, false, fmt.Sprintf("step %q was skipped", d)
		default:
			return true, false, fmt.Sprintf("step %q %s", d, s.Status)
		}
	}
	return true, true, ""
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// start renders a step and triggers its pipeline. The step is saved as
// starting before the trigger, so a crash in between is recovered by
// correlate instead of starting the pipeline twice.
func (e *Engine) start(ctx context.Context, run *Run, i int, def Step) {
	fail := func(msg string) {
		now := e.now()
		e.setStep(ctx, run, i, func(s *StepState) {
			s.Status, s.Message, s.FinishedAt = StepFailed, msg, now
		})
	}
	vals := run.values()
	ref, err := Render(def.Ref, vals)
	if err != nil {
		fail("ref: " + err.Error())
		return
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		fail("ref is empty")
		return
	}
	inputs, err := renderMap(def.Inputs, vals)
	if err != nil {
		fail("inputs: " + err.Error())
		return
	}
	variables, err := renderMap(def.Variables, vals)
	if err != nil {
		fail("variables: " + err.Error())
		return
	}
	conn, prov, err := e.provider(ctx, def.Connection)
	if err != nil {
		if ctx.Err() == nil {
			fail(err.Error())
		}
		return
	}
	now := e.now()
	if !e.setStep(ctx, run, i, func(s *StepState) {
		s.Status, s.Ref, s.TriggeredAt, s.Message = StepStarting, ref, now, ""
	}) {
		return
	}
	pr, err := prov.Trigger(ctx, conn, providers.TriggerRequest{PipelineID: def.Pipeline, Ref: ref, Inputs: inputs, Variables: variables})
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down; correlate decides after the restart.
			return
		}
		e.log.Warn("stream step trigger failed", "stream", run.Stream, "run", run.ID, "step", def.ID, "connection", def.Connection, "error", err)
		fail("trigger failed: " + err.Error())
		return
	}
	e.log.Info("stream step triggered", "stream", run.Stream, "run", run.ID, "step", def.ID, "connection", def.Connection,
		"pipeline", def.Pipeline, "ref", ref, "inputs", sortedKeys(inputs), "variables", sortedKeys(variables), "providerRun", pr.ID)
	if pr.ID == "" {
		return
	}
	e.setStep(ctx, run, i, func(s *StepState) {
		s.Status = StepRunning
		applyProviderRun(s, pr)
	})
}

func renderMap(m map[string]string, vals map[string]string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		v, err := Render(m[k], vals)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

func applyProviderRun(s *StepState, pr *providers.Run) {
	if pr.ID != "" {
		s.RunID = pr.ID
	}
	if pr.Number != 0 {
		s.RunNumber = pr.Number
	}
	if pr.WebURL != "" {
		s.URL = pr.WebURL
	}
	if pr.CommitSHA != "" {
		s.SHA = pr.CommitSHA
	}
	if pr.Status != "" {
		s.ProviderStatus = string(pr.Status)
	}
}

// correlate finds the pipeline run of a step whose trigger did not return
// a run id (or whose id was not saved).
func (e *Engine) correlate(ctx context.Context, run *Run, i int, def Step) {
	s := run.Steps[i]
	expired := e.now().Sub(s.TriggeredAt) > e.cfg.StartGrace
	conn, prov, err := e.provider(ctx, def.Connection)
	if err == nil {
		var runs []providers.Run
		runs, err = prov.ListRuns(ctx, conn, providers.RunFilter{PipelineID: def.Pipeline, Ref: s.Ref, Limit: correlationRuns})
		if err == nil {
			if pr := pickRun(run, i, def, runs); pr != nil {
				e.setStep(ctx, run, i, func(s *StepState) {
					s.Status = StepRunning
					s.Message = ""
					applyProviderRun(s, pr)
				})
				return
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	if !expired {
		return
	}
	msg := "the pipeline was triggered but its run could not be found"
	if err != nil {
		msg += ": " + err.Error()
	}
	now := e.now()
	e.setStep(ctx, run, i, func(s *StepState) {
		s.Status, s.Message, s.FinishedAt = StepFailed, msg, now
	})
}

// pickRun returns the earliest run created after the step was triggered
// that no other step of the run already owns.
func pickRun(run *Run, i int, def Step, runs []providers.Run) *providers.Run {
	defs := run.StepDefs()
	claimed := map[string]bool{}
	for j, s := range run.Steps {
		if j != i && s.RunID != "" && defs[s.ID].Connection == def.Connection {
			claimed[s.RunID] = true
		}
	}
	since := run.Steps[i].TriggeredAt.Add(-correlationSkew)
	var best *providers.Run
	for j := range runs {
		pr := &runs[j]
		if pr.ID == "" || claimed[pr.ID] || pr.CreatedAt.IsZero() || pr.CreatedAt.Before(since) {
			continue
		}
		if best == nil || pr.CreatedAt.Before(best.CreatedAt) {
			best = pr
		}
	}
	return best
}

// poll refreshes a running step and enforces its timeout.
func (e *Engine) poll(ctx context.Context, run *Run, i int, def Step) {
	cur := run.Steps[i]
	next := cur
	now := e.now()
	conn, prov, err := e.provider(ctx, def.Connection)
	switch {
	case errors.Is(err, connections.ErrNotFound):
		next.Status, next.FinishedAt = StepFailed, now
		next.Message = fmt.Sprintf("connection %q no longer exists", def.Connection)
	case err != nil:
		if ctx.Err() != nil {
			return
		}
		next.Message = "could not check the run: " + err.Error()
	default:
		d, err := prov.GetRun(ctx, conn, cur.RunID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			next.Message = "could not check the run: " + err.Error()
			break
		}
		applyProviderRun(&next, &d.Run)
		next.Message = ""
		switch d.Status {
		case providers.StatusSuccess:
			next.Status, next.FinishedAt = StepSucceeded, now
		case providers.StatusFailed:
			next.Status, next.FinishedAt, next.Message = StepFailed, now, "the pipeline failed"
		case providers.StatusCanceled:
			next.Status, next.FinishedAt, next.Message = StepCancelled, now, "the pipeline was cancelled"
		case providers.StatusSkipped:
			next.Status, next.FinishedAt, next.Message = StepSkipped, now, "the provider skipped the pipeline"
		case providers.StatusManual:
			next.Message = "waiting for a manual action"
		}
	}
	if next.Status == StepRunning {
		if limit := stepTimeout(def, e.cfg.StepTimeout); now.Sub(cur.TriggeredAt) > limit {
			if err == nil {
				if cerr := prov.CancelRun(ctx, conn, cur.RunID); cerr != nil && ctx.Err() == nil {
					e.log.Warn("could not cancel timed out stream step", "stream", run.Stream, "run", run.ID, "step", def.ID, "error", cerr)
				}
			}
			next.Status, next.FinishedAt = StepFailed, now
			next.Message = fmt.Sprintf("timed out after %s", limit)
		}
	}
	if next != cur {
		e.setStep(ctx, run, i, func(s *StepState) { *s = next })
	}
}

func stepTimeout(def Step, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(def.Timeout); err == nil && d > 0 {
		return d
	}
	return fallback
}

// cancelRun stops the active pipelines of a run and finishes it.
func (e *Engine) cancelRun(ctx context.Context, run *Run, defs map[string]Step) {
	for _, s := range run.Steps {
		if (s.Status != StepRunning && s.Status != StepStarting) || s.RunID == "" {
			continue
		}
		conn, prov, err := e.provider(ctx, defs[s.ID].Connection)
		if err == nil {
			err = prov.CancelRun(ctx, conn, s.RunID)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Warn("could not cancel stream step", "stream", run.Stream, "run", run.ID, "step", s.ID, "error", err)
		}
	}
	now := e.now()
	err := e.saveRun(ctx, run, func(r *Run) {
		for i := range r.Steps {
			s := &r.Steps[i]
			switch s.Status {
			case StepRunning, StepStarting:
				s.Status, s.FinishedAt, s.Message = StepCancelled, now, "the stream run was cancelled"
				if s.RunID == "" {
					s.Message = "the stream run was cancelled; the pipeline run was not identified and may still be running"
				}
			case StepPending:
				s.Status, s.FinishedAt, s.Message = StepSkipped, now, "the stream run was cancelled"
			}
		}
		r.Status, r.FinishedAt = RunCancelled, now
		r.Message = "cancelled"
		if r.CancelledBy != "" {
			r.Message = "cancelled by " + r.CancelledBy
		}
	})
	if err != nil {
		e.logSaveError(ctx, run, err)
		return
	}
	e.log.Info("stream run cancelled", "stream", run.Stream, "run", run.ID, "user", run.CancelledBy)
	e.prune(ctx, run.Stream)
}

// finishIfDone completes a run whose steps have all finished.
func (e *Engine) finishIfDone(ctx context.Context, run *Run, defs map[string]Step) {
	for _, s := range run.Steps {
		if !StepFinished(s.Status) {
			return
		}
	}
	status, msg := RunSucceeded, ""
	for _, s := range run.Steps {
		if failedHard(s, defs[s.ID]) {
			status, msg = RunFailed, fmt.Sprintf("step %q %s", s.ID, s.Status)
			break
		}
	}
	now := e.now()
	if err := e.saveRun(ctx, run, func(r *Run) {
		r.Status, r.Message, r.FinishedAt = status, msg, now
	}); err != nil {
		e.logSaveError(ctx, run, err)
		return
	}
	e.log.Info("stream run finished", "stream", run.Stream, "run", run.ID, "status", status)
	e.prune(ctx, run.Stream)
}

func (e *Engine) prune(ctx context.Context, stream string) {
	if n, err := PruneRuns(ctx, e.runs, stream, e.cfg.History); err != nil {
		if ctx.Err() == nil {
			e.log.Warn("could not prune stream runs", "stream", stream, "error", err)
		}
	} else if n > 0 {
		e.log.Debug("pruned stream runs", "stream", stream, "deleted", n)
	}
}

func (e *Engine) provider(ctx context.Context, name string) (*connections.Connection, providers.Provider, error) {
	c, err := e.conns.Get(ctx, name)
	if err != nil {
		if errors.Is(err, connections.ErrNotFound) {
			return nil, nil, fmt.Errorf("connection %q: %w", name, err)
		}
		return nil, nil, err
	}
	p, err := e.provs.Get(c.Provider)
	if err != nil {
		return nil, nil, err
	}
	return c, p, nil
}

// errStale means the stored run moved past the state a change was based on
// (another backend instance got there first, e.g. during a rolling update).
var errStale = errors.New("stream run changed elsewhere")

// save applies fn to the run and stores it. On a conflict (usually the API
// setting CancelRequested) fn is applied to a fresh copy; fn returns
// errStale when that copy no longer matches what the change was based on.
func (e *Engine) save(ctx context.Context, run *Run, fn func(*Run) error) error {
	cp := cloneRun(run)
	if err := fn(cp); err != nil {
		return err
	}
	updated, err := e.runs.Update(ctx, cp)
	if errors.Is(err, ErrRunConflict) {
		updated, err = Mutate(ctx, e.runs, run.Stream, run.ID, func(r *Run) error {
			if len(r.Steps) != len(run.Steps) {
				return fmt.Errorf("stream run %s changed shape", run.ID)
			}
			return fn(r)
		})
	}
	if err != nil {
		return err
	}
	*run = *updated
	return nil
}

// saveRun changes a run that must still be running.
func (e *Engine) saveRun(ctx context.Context, run *Run, fn func(*Run)) error {
	return e.save(ctx, run, func(r *Run) error {
		if r.Status != RunRunning {
			return errStale
		}
		fn(r)
		return nil
	})
}

// setStep changes step i only if it is still as this Engine last saw it,
// so two instances never trigger the same step twice.
func (e *Engine) setStep(ctx context.Context, run *Run, i int, fn func(*StepState)) bool {
	seen := run.Steps[i]
	err := e.save(ctx, run, func(r *Run) error {
		if r.Status != RunRunning || r.Steps[i] != seen {
			return errStale
		}
		fn(&r.Steps[i])
		return nil
	})
	if err != nil {
		e.logSaveError(ctx, run, err)
		return false
	}
	return true
}

func (e *Engine) logSaveError(ctx context.Context, run *Run, err error) {
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, errStale):
		e.log.Debug("stream run changed elsewhere; skipping", "stream", run.Stream, "run", run.ID)
	default:
		e.log.Warn("could not save stream run", "stream", run.Stream, "run", run.ID, "error", err)
	}
}
