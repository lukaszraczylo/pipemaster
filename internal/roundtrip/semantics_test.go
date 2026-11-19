package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	ghaWorkflow = ".github/workflows/ci.yml"
	gitlabCI    = ".gitlab-ci.yml"
)

// convert runs one file through Parse and Emit of two providers, without metadata.
func convert(t *testing.T, from, to, name, text string) (string, *report.Report) {
	t.Helper()
	proj, _, err := prov(t, from).Parse(fstest.MapFS{name: &fstest.MapFile{Data: []byte(text)}})
	if err != nil {
		t.Fatalf("parse %s: %v", from, err)
	}
	files, rep, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatalf("emit %s: %v", to, err)
	}
	var sb strings.Builder
	for _, n := range names(files) {
		sb.WriteString("### " + n + "\n" + string(files[n]))
	}
	return sb.String(), rep
}

func lostMentions(rep *report.Report, substr string) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, substr) || strings.Contains(e.IRPath, substr) {
			return true
		}
	}
	return false
}

// jobBlock returns the lines of a top-level GHA job, up to the next two-space-indented key.
func jobBlock(doc, name string) string {
	_, after, ok := strings.Cut(doc, "\n  "+name+":\n")
	if !ok {
		return ""
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if l != "" && !strings.HasPrefix(l, "   ") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestGitlabConditionsWithoutIRFormDisableGHAJobs(t *testing.T) {
	const src = `stages: [build, deploy]
build:
  stage: build
  script: [make]
  only: [main]
deploy:
  stage: deploy
  script: [./deploy.sh]
  except:
    - tags
  when: delayed
  start_in: 30 minutes
`
	doc, rep := convert(t, provider.GitLab, provider.GHA, gitlabCI, src)
	for _, job := range []string{"build", "deploy"} {
		if !strings.Contains(jobBlock(doc, job), "if: false") {
			t.Errorf("job %s has no fail-safe guard:\n%s", job, doc)
		}
	}
	if !lostMentions(rep, "build") || !lostMentions(rep, "deploy") {
		t.Errorf("report does not name both jobs: %+v", rep.Entries)
	}
}

func TestGitlabWorkflowRulesWithoutTriggerFormDisableGHAJobs(t *testing.T) {
	const src = `workflow:
  rules:
    - if: $CI_COMMIT_TAG
      when: never
    - when: always
deploy:
  script: [./deploy.sh]
`
	doc, _ := convert(t, provider.GitLab, provider.GHA, gitlabCI, src)
	if !strings.Contains(jobBlock(doc, "deploy"), "if: false") {
		t.Errorf("deploy runs unguarded:\n%s", doc)
	}
}

func TestGitlabOnlyExceptSurviveGitlabRoundTrip(t *testing.T) {
	const src = `build:
  script: [make]
  only:
    - main
  except:
    - tags
workflow:
  rules:
    - if: $CI_COMMIT_TAG
      when: never
    - when: always
`
	out, _ := convert(t, provider.GitLab, provider.GitLab, gitlabCI, src)
	for _, w := range []string{"only:", "- main", "except:", "- tags", "if: $CI_COMMIT_TAG", "when: never"} {
		if !strings.Contains(out, w) {
			t.Errorf("round trip lost %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "rules:\n    - when: never\n  script") {
		t.Errorf("job gained a never rule:\n%s", out)
	}
}

func onBlock(doc string) string {
	_, after, ok := strings.Cut(doc, "\non:\n")
	if !ok {
		return ""
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if l != "" && !strings.HasPrefix(l, " ") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestUnfilteredGHAPushKeepsTagsInGitlab(t *testing.T) {
	const src = `on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
`
	doc, _ := convert(t, provider.GHA, provider.GitLab, ghaWorkflow, src)
	if strings.Contains(doc, "workflow:") {
		t.Errorf("a push without filters must run on branches and tags; GitLab's default does, a workflow rule would narrow it:\n%s", doc)
	}
}

func TestGitlabBranchOnlyRuleStaysBranchOnlyInGHA(t *testing.T) {
	const src = `workflow:
  rules:
    - if: $CI_COMMIT_BRANCH
deploy:
  script: [./deploy.sh]
`
	doc, _ := convert(t, provider.GitLab, provider.GHA, gitlabCI, src)
	on := onBlock(doc)
	if !strings.Contains(on, "branches:") || strings.Contains(on, "tags:") {
		t.Errorf("branch-only pipeline must name branches and no tags:\n%s", doc)
	}
}

func TestGitlabPushSourceRunsOnTagsInGHA(t *testing.T) {
	const src = `workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "push"
deploy:
  script: [./deploy.sh]
`
	doc, _ := convert(t, provider.GitLab, provider.GHA, gitlabCI, src)
	on := onBlock(doc)
	if !strings.Contains(on, "push:") || strings.Contains(on, "branches:") || strings.Contains(on, "tags:") {
		t.Errorf("a push source is a bare GHA push, which fires for branches and tags:\n%s", doc)
	}
}
