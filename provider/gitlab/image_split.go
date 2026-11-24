package gitlab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	splitExpire   = "1 day"
	workspacePath = "."
	gitExclude    = ".git/**/*"
	guardStepID   = "pipemaster-split-guard"
	markStepID    = "pipemaster-split-ok"
	markDir       = ".pipemaster-split"
	markHashLen   = 8
	markExclude   = markDir + "/**/*"
	splitStrategy = "one job per run of steps that share an image, chained by needs; the workspace passes on as artifacts"
	liftStrategy  = "the shared image of the steps is the job image"
	splitCaveat   = "GitLab sets the image per job, so the job is split into %d jobs (%s) chained by needs; each job hands the project directory minus .git to the next as artifacts that expire in " + splitExpire +
		", so the artifact size limit of the instance applies, files outside the project directory do not carry over, a file that an earlier job deletes comes back from the checkout in the next job, and every job repeats the services and variables of the source job. " +
		"Timeout and retry apply to each job, so the total time can reach the timeout times the job count and a retry reruns only the failed job. " +
		"An allow_failure setting applies to every job, and each later job first checks a marker file of the job before it and ends with success when the marker is missing"
)

// imageSplitArg is the marker argument of one job that came from a job whose steps ran in several images.
type imageSplitArg struct {
	Container *ir.Image `json:"container,omitempty"`
	Job       string    `json:"job"`
	Parts     []string  `json:"parts"`
	Own       []string  `json:"own,omitempty"`
}

// splitPart tells the emitter which source job an emitted job stands for.
type splitPart struct {
	// path is the IR path of the source job, which the ledger and the step markers use.
	path string
	// imgPath is the step whose image the job carries, where a lost image reference is reported.
	imgPath string
	arg     imageSplitArg
}

// splitDenied are the Ext keys of a job that a split would not keep exact.
var splitDenied = []string{extNeedsKey, ExtNeedsExtra, extDeps, ExtDerivedNeeds, extParallel, "trigger", extEnvironment,
	"rules", ExtRulesAlways, ExtRulesFP, ExtRulesWhen, ExtNoScript}

func sameImage(a, b *ir.Image) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Ref == b.Ref && slices.Equal(a.Entrypoint, b.Entrypoint)
}

func isScript(st *ir.Step) bool { return st.Kind == ir.StepScript }

// isLostKind is a step GitLab drops whole, so its image cannot decide where a job splits.
func isLostKind(st *ir.Step) bool {
	return st.Kind != ir.StepScript && (st.Action == nil || st.Kind == ir.StepPipelineCall)
}

// effImage is the image a script step runs in.
func effImage(j *ir.Job, st *ir.Step) *ir.Image {
	if st.Image != nil {
		return st.Image
	}
	return j.Container
}

// imageRun is a run of consecutive steps that share an image.
type imageRun struct {
	img   *ir.Image
	steps []*ir.Step
}

// denySplit says why j cannot take part in an image split; "" means it can.
func denySplit(j *ir.Job) string {
	switch {
	case j.Matrix != nil:
		return "the job has a matrix"
	case len(j.Outputs) > 0:
		return "the job has outputs"
	case j.Environment != nil:
		return "the job has an environment"
	case j.Manual && j.AllowFailure:
		return "the job is manual and allowed to fail"
	}
	if j.If != nil {
		if _, status, ok := splitStatus(j.If); !ok || (status != "" && status != "success") {
			return "the job condition depends on the status of other jobs"
		}
	}
	own := extOf(j.Ext)
	for _, k := range splitDenied {
		if _, has := own[k]; has {
			return "the job has the GitLab key " + k
		}
	}
	for _, st := range j.Steps {
		if st != nil && !isScript(st) && !isLostKind(st) {
			return "step " + st.ID + " maps to a GitLab job keyword"
		}
	}
	return ""
}

// planRuns groups the script steps by image. Steps GitLab drops whole join the run before them.
func planRuns(j *ir.Job) (runs []imageRun, why string) {
	var lead []*ir.Step
	for _, st := range j.Steps {
		if st == nil {
			continue
		}
		if isLostKind(st) {
			if len(runs) == 0 {
				lead = append(lead, st)
			} else {
				runs[len(runs)-1].steps = append(runs[len(runs)-1].steps, st)
			}
			continue
		}
		img := effImage(j, st)
		if n := len(runs); n > 0 && sameImage(runs[n-1].img, img) {
			runs[n-1].steps = append(runs[n-1].steps, st)
			continue
		}
		runs = append(runs, imageRun{img: img, steps: append(lead, st)})
		lead = nil
	}
	for k, r := range runs[min(1, len(runs)):] {
		for _, st := range r.steps {
			if st.If == nil {
				continue
			}
			if _, status, ok := splitStatus(st.If); !ok || (status != "" && status != "success") {
				return nil, "step " + st.ID + " must run after a failure, and a later job never starts when an earlier one fails (run " + strconv.Itoa(k+2) + " has its own image)"
			}
		}
	}
	return runs, ""
}

// splitImages rewrites pl so that no job holds script steps of two images: a job whose steps share one image
// takes it as the job image, and any other job becomes a chain of jobs, one for each run of steps that share one.
// It returns pl itself when nothing changes.
func (e *emitter) splitImages(pl *ir.Pipeline) *ir.Pipeline {
	if e.fold != nil {
		return pl
	}
	taken := map[string]bool{}
	for _, j := range allJobs(pl) {
		taken[j.ID] = true
	}
	var jobs []*ir.Job
	last := map[string]string{}
	changed := false
	for _, j := range pl.Jobs {
		parts := e.planSplit(pl, j, taken)
		if parts == nil {
			jobs = append(jobs, j)
			continue
		}
		changed = true
		jobs = append(jobs, parts...)
		last[j.ID] = parts[len(parts)-1].ID
	}
	if !changed {
		return pl
	}
	cp := *pl
	cp.Jobs = e.remapNeeds(jobs, last)
	cp.Finally = e.remapNeeds(pl.Finally, last)
	return &cp
}

// remapNeeds points every need on a split job at its last part, copying only the jobs that change.
func (e *emitter) remapNeeds(jobs []*ir.Job, last map[string]string) []*ir.Job {
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		out[i] = j
		if j == nil || !slices.ContainsFunc(j.Needs, func(n ir.Need) bool { _, ok := last[n.JobID]; return ok }) {
			continue
		}
		cp := *j
		cp.Needs = slices.Clone(j.Needs)
		for k, n := range cp.Needs {
			if to, ok := last[n.JobID]; ok {
				cp.Needs[k].JobID = to
			}
		}
		if s, ok := e.splits[j]; ok {
			e.splits[&cp] = s
		}
		out[i] = &cp
	}
	return out
}

// planSplit builds the jobs that replace j, or returns nil when j stays as it is.
func (e *emitter) planSplit(pl *ir.Pipeline, j *ir.Job, taken map[string]bool) []*ir.Job {
	if j == nil || !slices.ContainsFunc(j.Steps, func(st *ir.Step) bool { return st != nil && isScript(st) && st.Image != nil }) {
		return nil
	}
	jp := jobPath(pl.ID, j.ID)
	why := denySplit(j)
	var runs []imageRun
	if why == "" {
		runs, why = planRuns(j)
	}
	if why != "" {
		e.noSplit[jp] = why
		return nil
	}
	ids := []string{j.ID}
	if len(runs) > 1 {
		ids = make([]string, len(runs))
		for i := range ids {
			id := j.ID + "-" + strconv.Itoa(i+1)
			for taken[id] {
				id += "-img"
			}
			taken[id], ids[i] = true, id
		}
	}
	e.noteSplit(jp, runs, ids)
	return e.buildParts(jp, j, runs, ids)
}

// noteSplit reports each step whose image the split carries.
func (e *emitter) noteSplit(jp string, runs []imageRun, ids []string) {
	first := true
	for i, r := range runs {
		for _, st := range r.steps {
			if !isScript(st) || st.Image == nil {
				continue
			}
			sp := stepPath(jp, st.ID)
			switch {
			case len(runs) == 1:
				e.emulate(sp, caps.StepImage, liftStrategy, "every script step of the job runs in this image")
			case first:
				e.emulate(sp, caps.StepImage, splitStrategy, splitText(len(runs), ids))
			default:
				e.emulate(sp, caps.StepImage, splitStrategy, "the step runs in the split job "+ids[i])
			}
			first = false
		}
	}
}

func splitText(n int, ids []string) string {
	return strings.Replace(strings.Replace(splitCaveat, "%d", strconv.Itoa(n), 1), "%s", strings.Join(ids, ", "), 1)
}

func (e *emitter) buildParts(jp string, j *ir.Job, runs []imageRun, ids []string) []*ir.Job {
	var out []*ir.Job
	for i, r := range runs {
		cp := *j
		cp.ID = ids[i]
		switch {
		case j.Name == j.ID:
			cp.Name = cp.ID
		case i > 0:
			cp.Name = ""
		}
		arg := imageSplitArg{Job: j.ID, Parts: ids}
		if j.Container != nil {
			c := *j.Container
			arg.Container = &c
		}
		cp.Steps = nil
		for _, st := range r.steps {
			c := *st
			if isScript(st) && st.Image != nil {
				arg.Own = append(arg.Own, st.ID)
				c.Image = nil
			}
			cp.Steps = append(cp.Steps, &c)
		}
		if r.img != nil {
			img := *r.img
			cp.Container = &img
		} else {
			cp.Container = nil
		}
		isLast := i == len(runs)-1
		if j.AllowFailure && len(runs) > 1 {
			prev := ""
			if i > 0 {
				prev = ids[i-1]
			}
			cp.Steps = append([]*ir.Step{startStep(ids[i], prev, isLast)}, cp.Steps...)
			if !isLast {
				cp.Steps = append(cp.Steps, markStep(ids[i]))
			}
		}
		if i > 0 {
			cp.Needs = []ir.Need{{JobID: ids[i-1]}}
			cp.Manual = false
		}
		if !isLast {
			cp.Artifacts = nil
		}
		cp.Ext = splitExt(j.Ext, isLast, sameImage(cp.Container, j.Container), j.AllowFailure && len(runs) > 1 && hasArtifacts(j))
		out = append(out, &cp)
		sp := splitPart{arg: arg, path: jp}
		for _, st := range r.steps {
			if isScript(st) {
				sp.imgPath = stepPath(jp, st.ID)
				break
			}
		}
		e.splits[&cp] = sp
	}
	return out
}

func singleQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// markPath is the marker file of a part, anchored at the project directory so a step that changes directory cannot move it.
func markPath(id string) string {
	return `"$` + "CI_PROJECT_DIR" + `"/` + singleQuote(markDir+"/"+markName(id)+".ok")
}

// markName is a file name for a part id: a safe spelling plus a short hash, so ids that spell the same stay apart.
func markName(id string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, id)
	sum := sha256.Sum256([]byte(id))
	return safe + "-" + hex.EncodeToString(sum[:])[:markHashLen]
}

// markStep records that every step of a part ran. Its line runs only if the lines before it succeeded.
func markStep(id string) *ir.Step {
	return &ir.Step{ID: markStepID, Kind: ir.StepScript, Script: []string{`mkdir -p "$` + "CI_PROJECT_DIR" + `"/` + singleQuote(markDir) + " && touch " + markPath(id)}}
}

// startStep opens a part. It removes the marker of the part, so a runner that keeps its build directory cannot
// reuse an old one. Then, when a part comes before it, it ends the job with success if that part left no marker:
// a job that is allowed to fail would otherwise run its later parts after a failure, which the source never does.
func startStep(id, prev string, last bool) *ir.Step {
	var lines []string
	if !last {
		lines = append(lines, "rm -f "+markPath(id))
	}
	if prev != "" {
		lines = append(lines, "[ -f "+markPath(prev)+" ] || { echo "+singleQuote("pipemaster: "+prev+" failed; the source job stops here, so the later steps are skipped")+"; exit 0; }")
	}
	return &ir.Step{ID: guardStepID, Kind: ir.StepScript, Script: lines}
}

func hasArtifacts(j *ir.Job) bool { return len(j.Artifacts) > 0 || extOf(j.Ext)[extArtifacts] != nil }

// withExclude returns a copy of an artifacts block with one more exclude pattern.
func withExclude(block any, pattern string) map[string]any {
	out := map[string]any{}
	if m, ok := block.(map[string]any); ok {
		out = maps.Clone(m)
	}
	list, _ := out["exclude"].([]any)
	out["exclude"] = append(slices.Clone(list), any(pattern))
	return out
}

// withoutExclude undoes withExclude. It returns nil when nothing else is left in the block.
func withoutExclude(block any, pattern string) any {
	m, ok := block.(map[string]any)
	if !ok {
		return block
	}
	out := maps.Clone(m)
	list, _ := out["exclude"].([]any)
	list = slices.DeleteFunc(slices.Clone(list), func(v any) bool { return v == pattern })
	if len(list) > 0 {
		out["exclude"] = list
	} else {
		delete(out, "exclude")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitExt copies the Ext of a source job for one part. A part that hands the workspace on writes that
// artifacts block, and the image extras stay with the part that has the source job image.
func splitExt(x ir.Ext, last, keepImage, hideMarks bool) ir.Ext {
	out := maps.Clone(x)
	own := maps.Clone(extOf(x))
	if own == nil && last && !hideMarks {
		return out
	}
	if own == nil {
		own = map[string]any{}
	}
	if !keepImage {
		delete(own, extImage)
	}
	if last && hideMarks {
		own[extArtifacts] = withExclude(own[extArtifacts], markExclude)
	}
	if !last {
		own[extArtifacts] = map[string]any{"paths": []any{workspacePath}, "exclude": []any{gitExclude}, "expire_in": splitExpire}
	}
	if out == nil {
		out = ir.Ext{}
	}
	out[dialect] = own
	return out
}

// splitMarkers tags a job that came from an image split, so Parse can join the chain again.
func (e *emitter) splitMarkers(pid string, j *ir.Job) []string {
	sp, ok := e.splits[j]
	if !ok {
		return nil
	}
	arg := sp.arg
	arg.Own = nil
	for _, st := range j.Steps {
		if slices.Contains(sp.arg.Own, st.ID) {
			arg.Own = append(arg.Own, st.ID)
		}
	}
	a, err := markerArg(arg)
	if err != nil {
		return nil
	}
	return []string{emitkit.Marker(caps.StepImage, jobPath(pid, j.ID)), a}
}

// mergeImageSplits joins the jobs of each marked chain into the source job and returns the markers it used.
func (s *state) mergeImageSplits(pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) map[ir.FeatureUse]bool {
	done := map[ir.FeatureUse]bool{}
	s.splitImg = map[*ir.Job]map[string]ir.Image{}
	byJob := map[string]map[string]imageSplitArg{}
	owner := map[ir.FeatureUse]string{}
	for _, u := range uses {
		if u.Feature != caps.StepImage || strings.Contains(u.IRPath, stepSep) || len(args[u]) != 1 {
			continue
		}
		var a imageSplitArg
		id, ok := strings.CutPrefix(u.IRPath, jobPrefix(pl))
		if !ok || json.Unmarshal([]byte(args[u][0]), &a) != nil || a.Job == "" || !slices.Contains(a.Parts, id) {
			continue
		}
		if byJob[a.Job] == nil {
			byJob[a.Job] = map[string]imageSplitArg{}
		}
		byJob[a.Job][id], owner[u] = a, a.Job
	}
	for _, name := range sortedKeys(byJob) {
		group := byJob[name]
		ref := group[sortedKeys(group)[0]]
		if !s.joinChain(pl, args, ref, group) {
			continue
		}
		for u, o := range owner {
			if o == name {
				done[u] = true
			}
		}
	}
	return done
}

func (s *state) joinChain(pl *ir.Pipeline, args map[ir.FeatureUse][]string, ref imageSplitArg, group map[string]imageSplitArg) bool {
	parts := make([]*ir.Job, len(ref.Parts))
	for i, id := range ref.Parts {
		a, marked := group[id]
		if !marked || !slices.Equal(a.Parts, ref.Parts) || a.Job != ref.Job {
			return false
		}
		if parts[i] = findJob(pl, id); parts[i] == nil {
			return false
		}
	}
	base, lastJob := parts[0], parts[len(parts)-1]
	var steps []*ir.Step
	for i, pj := range parts {
		own := group[ref.Parts[i]].Own
		for _, st := range pj.Steps {
			if st.ID != guardStepID && st.ID != markStepID {
				steps = append(steps, st)
			}
		}
		if pj.Container != nil {
			for _, id := range own {
				if s.splitImg[base] == nil {
					s.splitImg[base] = map[string]ir.Image{}
				}
				s.splitImg[base][id] = *pj.Container
			}
		}
	}
	base.Steps = steps
	base.Artifacts, base.Outputs, base.AllowFailure = lastJob.Artifacts, lastJob.Outputs, lastJob.AllowFailure
	if lastJob != base {
		dropExt(&base.Ext, extArtifacts)
		if a, ok := extOf(lastJob.Ext)[extArtifacts]; ok {
			if a = withoutExclude(a, markExclude); a != nil {
				setExt(&base.Ext, extArtifacts, a)
			}
		}
	}
	base.Container = nil
	if ref.Container != nil {
		c := *ref.Container
		base.Container = &c
	}
	remapPairs(args, ref, group)
	rename := map[string]string{}
	for _, id := range ref.Parts {
		rename[id] = ref.Job
	}
	base.ID = ref.Job
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool { return j != base && slices.Contains(parts, j) })
	for _, j := range allJobs(pl) {
		j.Needs = renameNeeds(j.Needs, rename)
	}
	return true
}

// remapPairs moves the conversion pairs of the jobs in a chain to the joined job. Step positions follow their
// step, the job fields come from the first job and the artifacts from the last, and a job image pair goes to
// each step that took the image.
func remapPairs(args map[ir.FeatureUse][]string, ref imageSplitArg, group map[string]imageSplitArg) {
	for u, a := range args {
		if u.Feature != caps.ScriptInterpolation {
			continue
		}
		pairs, ok := readPairs(a)
		if !ok {
			continue
		}
		var out []refPair
		for _, p := range pairs {
			out = append(out, remapPair(p, ref, group)...)
		}
		if b, err := markerArg(out); err == nil {
			args[u] = []string{strings.TrimPrefix(b, emitkit.ArgPrefix)}
		}
	}
}

func remapPair(p refPair, ref imageSplitArg, group map[string]imageSplitArg) []refPair {
	for i, id := range ref.Parts {
		prefix := "job/" + id + "/"
		rest, ok := strings.CutPrefix(p.P, prefix)
		if !ok {
			continue
		}
		to := "job/" + ref.Job + "/"
		switch {
		case strings.HasPrefix(rest, "step/"):
			p.P = to + rest
			return []refPair{p}
		case rest == "image":
			var out []refPair
			for _, own := range group[id].Own {
				q := p
				q.P = to + "step/" + own + "/image"
				out = append(out, q)
			}
			return out
		default:
			art := strings.HasPrefix(rest, "artifact/")
			if (art && i == len(ref.Parts)-1) || (!art && i == 0) {
				p.P = to + rest
				return []refPair{p}
			}
		}
		return nil
	}
	return []refPair{p}
}

// applySplitImages gives the steps that ran in their own image that image again. It runs after the steps
// that moved to after_script are back, because those exist only then.
func (s *state) applySplitImages() {
	for j, imgs := range s.splitImg {
		for _, st := range j.Steps {
			if img, ok := imgs[st.ID]; ok {
				c := img
				c.Entrypoint = slices.Clone(img.Entrypoint)
				st.Image = &c
			}
		}
	}
}

// renameNeeds renames needs and removes the repeats the rename makes.
func renameNeeds(needs []ir.Need, rename map[string]string) []ir.Need {
	var out []ir.Need
	for _, n := range needs {
		if to, ok := rename[n.JobID]; ok {
			n.JobID = to
		}
		if !slices.ContainsFunc(out, func(o ir.Need) bool { return o.JobID == n.JobID }) {
			out = append(out, n)
		}
	}
	return out
}

// unmergedSplit reports a split marker whose chain could not be joined.
func (s *state) unmergedSplit(u ir.FeatureUse) {
	s.rep.Add(report.Entry{
		IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as separate jobs",
		Detail: "marker not collapsed: the jobs of the image split are edited or missing",
	})
}
