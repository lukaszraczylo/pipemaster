//go:build behave

package behave

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

// engineSlots caps the parallel runs per engine. Kiri has one VM, so the caps stay small.
var engineSlots = map[string]int{
	provGHA: 1, provGitLab: 2, provWoodpecker: 3, provDrone: 3, provTekton: 4, provArgo: 4, provConcourse: 2,
}

// maxParallel caps the cells that run at once, whatever the per-engine caps allow.
const maxParallel = 8

const markerSHA = "env.sha:"

// Options narrows a run.
type Options struct {
	Only    *regexp.Regexp
	Log     func(format string, args ...any)
	Targets []string
}

// Runner executes the matrix.
type Runner struct {
	env   *Env
	kube  *Kube
	conc  *Concourse
	sem   map[string]chan struct{}
	times map[string]*EngineTime
	cap   chan struct{}
	opts  Options
	mu    sync.Mutex
}

// EngineTime sums the engine run time of a run.
type EngineTime struct {
	Total time.Duration
	Runs  int
}

// NewRunner builds a runner on env.
func NewRunner(env *Env, opts Options) *Runner {
	r := &Runner{env: env, kube: NewKube(env), conc: NewConcourse(env), opts: opts, sem: map[string]chan struct{}{}, cap: make(chan struct{}, maxParallel), times: map[string]*EngineTime{}}
	for p, n := range engineSlots {
		r.sem[p] = make(chan struct{}, n)
	}
	return r
}

// Close stops what the run started: the cluster and the Concourse containers.
func (r *Runner) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r.conc.Stop(ctx)
	r.kube.Stop(ctx)
	r.env.RemoveBaked(ctx)
}

// Times returns the per-engine run times.
func (r *Runner) Times() map[string]EngineTime {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]EngineTime{}
	for k, v := range r.times {
		out[k] = *v
	}
	return out
}

func (r *Runner) addTime(provider string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.times[provider]
	if t == nil {
		t = &EngineTime{}
		r.times[provider] = t
	}
	t.Total += d
	t.Runs++
}

// exec runs one workspace on the engine of provider, under the engine's slot cap.
func (r *Runner) exec(ctx context.Context, provider string, src, dst string, ev Event, converted bool) (*Trace, error) {
	r.cap <- struct{}{}
	defer func() { <-r.cap }()
	r.sem[provider] <- struct{}{}
	defer func() { <-r.sem[provider] }()
	eng := r.env.EngineFor(provider, r.kube, r.conc)
	if eng == nil {
		return nil, blocked("no engine for %s", provider)
	}
	var (
		tr  *Trace
		err error
	)
	for attempt := 0; attempt < flakeAttempts; attempt++ {
		var w *Workspace
		if w, err = r.env.NewWorkspace(ctx, src, dst, ev); err != nil {
			return nil, err
		}
		w.Converted = converted
		tr, err = eng.Run(ctx, w)
		if tr != nil {
			r.addTime(provider, tr.Duration)
			_ = os.WriteFile(dst+".log", []byte(tr.Log), 0o600) // #nosec G703 -- debug aid under the run work dir; a missing log does not change the result
		}
		if err != nil || tr.Exit == 0 || !flaky(tr.Log) {
			break
		}
	}
	return tr, err
}

// flakeAttempts bounds the retries of a run that died of a known Kiri container runtime race.
const flakeAttempts = 3

var flakeRe = regexp.MustCompile(`network \S+ not found|layer sha256:\w+ is gone|No such container: \w+|address already in use|is already in use|network with name \S+ already exists`)

func flaky(log string) bool { return flakeRe.MatchString(log) }

type srcRun struct {
	trace *Trace
	err   error
}

func (r *Runner) wantTarget(t string) bool {
	return len(r.opts.Targets) == 0 || slices.Contains(r.opts.Targets, t)
}

// Run executes every fixture on its source engine, converts it to every other target and compares.
func (r *Runner) Run(ctx context.Context, fixtures []Fixture) ([]Result, error) {
	if err := r.env.RequireKiriContext(ctx); err != nil {
		return nil, err
	}
	if err := r.env.CheckDocker(ctx); err != nil {
		return nil, err
	}
	var picked []Fixture
	for _, fx := range fixtures {
		if r.opts.Only == nil || r.opts.Only.MatchString(fx.Source+"/"+fx.Name) {
			picked = append(picked, fx)
		}
	}
	srcs := r.runSources(ctx, picked)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []Result
	)
	// Concourse runs in a second wave, after the cluster stops: its privileged worker and the cluster share one VM, and
	// the two together starve the cluster.
	for _, wave := range [][]string{waveOne(), {provConcourse}} {
		for _, fx := range picked {
			for _, tgt := range wave {
				if tgt == fx.Source || !r.wantTarget(tgt) {
					continue
				}
				wg.Add(1)
				go func(fx Fixture, tgt string) {
					defer wg.Done()
					res := r.runTarget(ctx, fx, tgt, srcs)
					mu.Lock()
					out = append(out, res...)
					mu.Unlock()
				}(fx, tgt)
			}
		}
		wg.Wait()
		r.kube.Stop(ctx)
	}
	return out, nil
}

// waveOne lists every target but Concourse.
func waveOne() []string {
	return slices.DeleteFunc(slices.Clone(Targets), func(t string) bool { return t == provConcourse })
}

func srcKey(fx Fixture, ev string) string { return fx.Source + "/" + fx.Name + "/" + ev }

func (r *Runner) runSources(ctx context.Context, fixtures []Fixture) map[string]*srcRun {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string]*srcRun{}
	)
	for _, fx := range fixtures {
		for _, evName := range fx.Events {
			wg.Add(1)
			go func(fx Fixture, evName string) {
				defer wg.Done()
				dst := filepath.Join(r.env.Work, "ws", fx.Source, fx.Name, "source-"+evName)
				tr, err := r.exec(ctx, fx.Source, fx.Dir, dst, events[evName], false)
				switch {
				case err == nil && tr.Exit != 0 && !fx.Failure:
					err = blocked("the source failed on its own engine (exit %d): %s", tr.Exit, diagnose(tr.Log))
				case err == nil && missingMarker(fx.Expect, tr.Markers) != "":
					err = blocked("the source engine did not reproduce marker %q: an engine limitation, not a converter result", missingMarker(fx.Expect, tr.Markers))
				case err == nil && len(tr.Markers) == 0 && !tr.NoPipeline:
					err = blocked("the source printed no marker on its own engine: %s", diagnose(tr.Log))
				}
				mu.Lock()
				out[srcKey(fx, evName)] = &srcRun{trace: tr, err: err}
				mu.Unlock()
				r.opts.Log("source %s %s: %s", srcKey(fx, evName), fx.Source, describe(tr, err))
			}(fx, evName)
		}
	}
	wg.Wait()
	return out
}

func describe(tr *Trace, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%d markers, exit %d, %s", len(tr.Markers), tr.Exit, tr.Duration.Round(time.Second))
}

func (r *Runner) runTarget(ctx context.Context, fx Fixture, tgt string, srcs map[string]*srcRun) []Result {
	base := Result{Source: fx.Source, Target: tgt, Fixture: fx.Name}
	conv, err := r.env.Convert(ctx, fx, tgt)
	var out []Result
	for _, evName := range fx.Events {
		res := base
		res.Event = evName
		src := srcs[srcKey(fx, evName)]
		switch {
		case err != nil:
			res.Status, res.Detail = StatusBlocked, "harness error: "+err.Error()
		case conv.Exit != 0:
			res.Status, res.Detail = StatusFail, "convert exit "+fmt.Sprint(conv.Exit)+": "+firstLine(conv.Stderr)
		case !conv.wroteTarget(tgt):
			res.Status, res.Detail = StatusFail, fmt.Sprintf("the converter wrote no %s file (files: %q); %s", tgt, conv.Files, conv.reportText())
		case src.err != nil:
			res.Status, res.Detail = StatusBlocked, src.err.Error()
		default:
			r.compareOne(ctx, fx, tgt, evName, conv, src.trace, &res)
		}
		r.opts.Log("%s -> %s %s %s: %s %s", fx.Source, tgt, fx.Name, evName, res.Status, res.Detail)
		out = append(out, res)
	}
	return out
}

func (r *Runner) compareOne(ctx context.Context, fx Fixture, tgt, evName string, conv *Converted, src *Trace, res *Result) {
	if why := limit(fx, tgt, evName); why != "" {
		r.limited(fx, tgt, evName, why, conv, src, res)
		return
	}
	dst := filepath.Join(r.env.Work, "ws", fx.Source, fx.Name, tgt+"-"+evName)
	tr, err := r.exec(ctx, tgt, conv.Dir, dst, events[evName], true)
	if err != nil {
		if b, ok := asBlocked(err); ok {
			res.Status, res.Detail = StatusBlocked, b.Reason
		} else {
			res.Status, res.Detail = StatusBlocked, "engine error: "+err.Error()
		}
		return
	}
	res.SourceTime, res.TargetTime = src.Duration, tr.Duration
	srcCmp, tgtCmp := src, tr
	if capsOf(tgt).noSHA || capsOf(fx.Source).noSHA {
		srcCmp, tgtCmp = withoutPrefix(src, markerSHA), withoutPrefix(tr, markerSHA)
	}
	res.Status, res.Detail, res.Explained = Judge(fx, evName, srcCmp, tgtCmp, conv.Report)
	if res.Status != StatusPass {
		if len(tr.Markers) == 0 {
			res.Detail += " | target log: " + diagnose(tr.Log)
		} else {
			res.Detail += fmt.Sprintf(" | target exit %d, markers %q", tr.Exit, tr.Markers)
		}
	}
}

var errLineRe = regexp.MustCompile(`(?i)error|fail|denied|not found|invalid|unknown|cannot|refused`)

// diagnose picks the first lines of a log that name an error, so a blocked cell shows its cause.
func diagnose(log string) string {
	var hits []string
	for _, l := range strings.Split(stripANSI(log), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && errLineRe.MatchString(l) && !strings.HasPrefix(l, "at ") {
			hits = append(hits, tail(l, 140))
			if len(hits) == 3 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return tail(strings.ReplaceAll(log, "\n", " / "), 200)
	}
	return strings.Join(hits, " / ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// missingMarker returns the first expected marker the source run did not print.
func missingMarker(expect, got []string) string {
	for _, e := range expect {
		if !slices.Contains(got, e) {
			return e
		}
	}
	return ""
}

func withoutPrefix(t *Trace, prefix string) *Trace {
	c := *t
	c.Markers = slices.DeleteFunc(slices.Clone(t.Markers), func(m string) bool { return strings.HasPrefix(m, prefix) })
	return &c
}

// limited settles a cell that the target engine cannot run. A pull request cell has three outcomes. The file
// reports the lost pull request trigger, so the cell stays blocked. The source ran nothing on the event, so it has
// no pull request trigger to report and the target runs nothing either, which the comparison judges. Otherwise
// the source ran work that the file neither runs nor reports, which fails the cell.
func (r *Runner) limited(fx Fixture, tgt, evName, why string, conv *Converted, src *Trace, res *Result) {
	if evName != EvPR || !eventUnsupported(tgt, evName) {
		res.Status, res.Detail = StatusBlocked, why
		return
	}
	switch {
	case hasVerdict(conv.Report, caps.TriggerPullRequest):
		res.Status, res.Detail = StatusBlocked, why
	case len(src.Markers) == 0:
		none := &Trace{Log: "the target engine has no event for this run", NoPipeline: true}
		res.SourceTime = src.Duration
		res.Status, res.Detail, res.Explained = Judge(fx, evName, src, none, conv.Report)
	default:
		res.Status, res.Detail = StatusFail, why+"; the source ran on the event but the converted file carries no verdict for "+string(caps.TriggerPullRequest)
	}
}

// hasVerdict is true when the report holds a lost, external or emulated entry for the feature.
func hasVerdict(rep *report.Report, f caps.Feature) bool {
	return rep != nil && slices.ContainsFunc(rep.Entries, func(e report.Entry) bool { return e.Feature == f && e.Level != caps.Native })
}
