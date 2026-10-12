package streams

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// StepTypeArgoCD waits until an Argo CD Application is synced and healthy.
const StepTypeArgoCD = "argocd"

const (
	// DefaultArgoTimeout is how long an argocd step waits by default.
	DefaultArgoTimeout = 30 * time.Minute
	// DegradedGrace is how long an Application may stay Degraded after it
	// reached the expected state before the step fails.
	DegradedGrace = 5 * time.Minute
	// argoSkew tolerates clock differences between Zea and Argo CD when
	// comparing a sync's finish time with the step's start.
	argoSkew = 5 * time.Second
	// maxImagesLen caps the images saved in a step state.
	maxImagesLen = 2000
)

// Argo CD health and sync statuses used by argocd steps.
const (
	HealthHealthy  = "Healthy"
	HealthDegraded = "Degraded"
	SyncSynced     = "Synced"
)

var (
	// ErrAppNotFound is returned when an Application does not exist.
	ErrAppNotFound = errors.New("application not found")
	// ErrAppForbidden is returned when Zea may not read an Application.
	ErrAppForbidden = errors.New("Zea is not allowed to read the application; check the chart's argocd.applicationNamespaces and RBAC")
)

// AppState is what an argocd step reads from an Application.
type AppState struct {
	Sync          string
	Health        string
	HealthMessage string
	// Revision is the git revision the Application is synced to.
	Revision string
	// Images are the images of the Application's live resources.
	Images []string
	// The last sync operation; OperationPhase is empty when none ran.
	OperationPhase      string
	OperationMessage    string
	OperationFinishedAt time.Time
}

// AppClient reads and refreshes Argo CD Applications.
type AppClient interface {
	GetApp(ctx context.Context, namespace, name string) (*AppState, error)
	// RefreshApp asks Argo CD to compare the Application with git now.
	RefreshApp(ctx context.Context, namespace, name string) error
}

var appNameRe = regexp.MustCompile(`^[a-z0-9]([-.a-z0-9]*[a-z0-9])?$`)

// IsArgoCD reports whether the step waits for an Argo CD Application.
func (s Step) IsArgoCD() bool { return s.Type == StepTypeArgoCD }

// ParseApp splits "<namespace>/<name>" or "<name>"; namespace is empty for
// the latter.
func ParseApp(v string) (namespace, name string, err error) {
	v = strings.TrimSpace(v)
	ns, n, found := strings.Cut(v, "/")
	if !found {
		ns, n = "", v
	}
	if (found && !appNameRe.MatchString(ns)) || !appNameRe.MatchString(n) || len(v) > 253 {
		return "", "", fmt.Errorf("invalid application %q: use <name> or <namespace>/<name>", v)
	}
	return ns, n, nil
}

// ImageMatches reports whether one of the images is the expected one:
// the same reference, the same without the registry, or the same tag when
// expected is only a tag.
func ImageMatches(expected string, images []string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return false
	}
	tagOnly := !strings.ContainsAny(expected, "/:@")
	for _, img := range images {
		switch {
		case img == expected,
			strings.HasSuffix(img, "/"+expected),
			tagOnly && strings.HasSuffix(img, ":"+expected):
			return true
		}
	}
	return false
}

// appReached reports whether the Application got the change the step waits
// for: the expected image is live, or (without an image) a sync finished
// after the step started. why tells what is still missing.
func appReached(st *AppState, started time.Time, image string) (bool, string) {
	if st.OperationPhase == "Running" || st.OperationPhase == "Terminating" {
		return false, "a sync is in progress"
	}
	if image != "" {
		if ImageMatches(image, st.Images) {
			return true, ""
		}
		return false, fmt.Sprintf("waiting for image %s to be deployed", image)
	}
	if !st.OperationFinishedAt.IsZero() && !st.OperationFinishedAt.Before(started.Add(-argoSkew)) {
		return true, ""
	}
	return false, "waiting for a sync that starts after this step"
}

// opFailedSince reports a sync operation that failed after the step started.
func opFailedSince(st *AppState, started time.Time) bool {
	if st.OperationPhase != "Failed" && st.OperationPhase != "Error" {
		return false
	}
	return !st.OperationFinishedAt.IsZero() && !st.OperationFinishedAt.Before(started.Add(-argoSkew))
}

func joinImages(images []string) string {
	sorted := slices.Clone(images)
	slices.Sort(sorted)
	out := strings.Join(slices.Compact(sorted), ",")
	if len(out) > maxImagesLen {
		out = out[:maxImagesLen]
		if i := strings.LastIndexByte(out, ','); i > 0 {
			out = out[:i]
		}
	}
	return out
}

// appRef resolves a rendered application against the allowed namespaces;
// the first one is the default.
func (e *Engine) appRef(app string) (namespace, name string, err error) {
	ns, n, err := ParseApp(app)
	if err != nil {
		return "", "", err
	}
	if len(e.cfg.AppNamespaces) == 0 {
		return "", "", errors.New("no Argo CD application namespaces are configured")
	}
	if ns == "" {
		ns = e.cfg.AppNamespaces[0]
	}
	if !slices.Contains(e.cfg.AppNamespaces, ns) {
		return "", "", fmt.Errorf("namespace %q is not one of the application namespaces Zea may watch (%s)", ns, strings.Join(e.cfg.AppNamespaces, ", "))
	}
	return ns, n, nil
}

// startArgo renders an argocd step, starts waiting and asks Argo CD to
// refresh the Application.
func (e *Engine) startArgo(ctx context.Context, run *Run, i int, def Step, fail func(string)) {
	vals := run.values()
	app, err := Render(def.App, vals)
	if err != nil {
		fail("app: " + err.Error())
		return
	}
	image, err := Render(def.Image, vals)
	if err != nil {
		fail("image: " + err.Error())
		return
	}
	image = strings.TrimSpace(image)
	if e.cfg.Apps == nil {
		fail("Argo CD access is not configured")
		return
	}
	ns, name, err := e.appRef(app)
	if err != nil {
		fail(err.Error())
		return
	}
	now := e.now()
	if !e.setStep(ctx, run, i, func(s *StepState) {
		s.Status, s.App, s.Image, s.TriggeredAt, s.RetryAt = StepRunning, ns+"/"+name, image, now, time.Time{}
		s.Message = "waiting for the application"
	}) {
		return
	}
	e.log.Info("stream step waiting for application", "stream", run.Stream, "run", run.ID, "step", def.ID, "app", ns+"/"+name, "image", image)
	if !def.NoRefresh {
		if err := e.cfg.Apps.RefreshApp(ctx, ns, name); err != nil && !errors.Is(err, ErrAppNotFound) {
			if ctx.Err() != nil {
				return
			}
			e.log.Warn("could not refresh application", "stream", run.Stream, "run", run.ID, "step", def.ID, "app", ns+"/"+name, "error", err)
		}
	}
	e.watchApp(ctx, run, i, def)
}

// watchApp checks the Application of a running argocd step.
func (e *Engine) watchApp(ctx context.Context, run *Run, i int, def Step) {
	cur := run.Steps[i]
	next := cur
	now := e.now()
	var st *AppState
	err := errors.New("Argo CD access is not configured")
	ns, name, _ := strings.Cut(cur.App, "/")
	if e.cfg.Apps != nil {
		st, err = e.cfg.Apps.GetApp(ctx, ns, name)
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return
	case errors.Is(err, ErrAppNotFound):
		next.Message = fmt.Sprintf("application %s does not exist", cur.App)
	case errors.Is(err, ErrAppForbidden):
		next.Status, next.FinishedAt, next.Message = StepFailed, now, err.Error()
	case err != nil:
		next.Message = "could not read the application: " + err.Error()
	default:
		next.Health, next.Sync, next.SHA, next.Images = st.Health, st.Sync, st.Revision, joinImages(st.Images)
		reached, why := appReached(st, cur.TriggeredAt, cur.Image)
		switch {
		case opFailedSince(st, cur.TriggeredAt):
			next.Status, next.FinishedAt = StepFailed, now
			next.Message = fmt.Sprintf("the sync %s", strings.ToLower(st.OperationPhase))
			if st.OperationMessage != "" {
				next.Message += ": " + st.OperationMessage
			}
		case !reached:
			next.DegradedAt, next.Message = time.Time{}, why
		case st.Health == HealthHealthy && st.Sync == SyncSynced:
			next.Status, next.FinishedAt, next.DegradedAt, next.Message = StepSucceeded, now, time.Time{}, ""
		case st.Health == HealthDegraded:
			if next.DegradedAt.IsZero() {
				next.DegradedAt = now
			}
			next.Message = "degraded"
			if st.HealthMessage != "" {
				next.Message += ": " + st.HealthMessage
			}
			if now.Sub(next.DegradedAt) > DegradedGrace {
				next.Status, next.FinishedAt = StepFailed, now
				next.Message = fmt.Sprintf("%s for more than %s", next.Message, DegradedGrace)
			}
		default:
			next.DegradedAt = time.Time{}
			next.Message = fmt.Sprintf("%s and %s", orUnknown(st.Sync), orUnknown(st.Health))
		}
	}
	if next.Status == StepRunning {
		if limit := stepTimeout(def, DefaultArgoTimeout); now.Sub(cur.TriggeredAt) > limit {
			next.Status, next.FinishedAt = StepFailed, now
			next.Message = fmt.Sprintf("timed out after %s; last state: %s", limit, next.Message)
		}
	}
	if next == cur || !e.setStep(ctx, run, i, func(s *StepState) { *s = next }) {
		return
	}
	if StepFinished(next.Status) {
		e.log.Info("stream application step finished", "stream", run.Stream, "run", run.ID, "step", def.ID, "app", cur.App,
			"status", next.Status, "message", next.Message)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}

// argoProblems checks the fields of an argocd step.
func argoProblems(step Step) []string {
	var out []string
	if step.Connection != "" || step.Pipeline != "" || step.Ref != "" || len(step.Inputs)+len(step.Variables) > 0 {
		out = append(out, "an argocd step has no connection, pipeline, ref, inputs or variables")
	}
	if step.Message != "" || step.NoSelfApproval {
		out = append(out, "only approval steps have a message or noSelfApproval")
	}
	if step.Retries != 0 || step.RetryDelay != "" {
		out = append(out, "an argocd step is not retried automatically; raise its timeout instead")
	}
	app := strings.TrimSpace(step.App)
	switch {
	case app == "":
		out = append(out, "app is required")
	case !strings.Contains(app, openDelim):
		if _, _, err := ParseApp(app); err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}
