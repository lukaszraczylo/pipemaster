package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func roundTripsExactly(t *testing.T, src string) string {
	t.Helper()
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	files, _ := emit(t, p)
	all := ""
	for _, b := range files {
		all += b
	}
	back, _, err := parseRepo(map[string]string{".argo/app.yaml": all})
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
	return all
}

func TestOneVolumeAtTwoMountPathsRoundTripsBothPaths(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: data, emptyDir: {}}
    container:
      image: alpine
      command: [sh, -c]
      args: ["cp /a/x /b/y"]
      volumeMounts:
      - {name: data, mountPath: /a}
      - {name: data, mountPath: /b}
`)
	all := roundTripsExactly(t, src)
	if !strings.Contains(all, "cp /a/x /b/y") {
		t.Errorf("both mount paths must return:\n%s", all)
	}
}

func TestTwoVolumesAtOneMountPathKeepTheirOwnDirectories(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: one, emptyDir: {}}
    - {name: two, emptyDir: {}}
    containerSet:
      containers:
      - name: a
        image: alpine
        command: [sh, -c]
        args: ["echo a > /data/f"]
        volumeMounts:
        - {name: one, mountPath: /data}
      - name: b
        image: alpine
        command: [sh, -c]
        args: ["echo b > /data/f"]
        volumeMounts:
        - {name: two, mountPath: /data}
`)
	all := roundTripsExactly(t, src)
	if strings.Count(all, "echo a > /data/f") != 1 || strings.Count(all, "echo b > /data/f") != 1 {
		t.Errorf("each container keeps its /data:\n%s", all)
	}
}

func TestMountPathInAWorkingDirectoryStepResolvesFromTheCheckoutRoot(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: data, emptyDir: {}}
    container:
      image: alpine
      workingDir: /data/app
      command: [sh, -c]
      args: ["cat /data/f"]
      volumeMounts:
      - {name: data, mountPath: /data}
`)
	pl, _ := parseYAML(t, src)
	var step *ir.Step
	for _, s := range pl.Jobs[0].Steps {
		if !strings.HasSuffix(s.ID, "-workspace-dirs") {
			step = s
		}
	}
	if step == nil || step.WorkDir != "pm-workspaces/data/app" || strings.Join(step.Script, "\n") != "sh -c 'cat ../../../pm-workspaces/data/f'" {
		t.Fatalf("step = %+v", step)
	}
	all := roundTripsExactly(t, src)
	if !strings.Contains(all, "cat /data/f") || !strings.Contains(all, "workingDir: /data/app") {
		t.Errorf("the mount path must return:\n%s", all)
	}
}

func TestConflictingMountHasAVerdictThatNamesTheEffect(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: data, emptyDir: {}}
    container:
      image: alpine
      command: [sh, -c]
      args: ["cp /a/x /b/y"]
      volumeMounts:
      - {name: data, mountPath: /a}
      - {name: data, mountPath: /b}
`)
	_, rep := parseYAML(t, src)
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.Detail, "mount of the volume data at /b") &&
			strings.Contains(e.Detail, "also mounted at /a") && strings.Contains(e.Detail, "no artifact carries") {
			return
		}
	}
	t.Errorf("no entry names the mount, the volume and the effect: %+v", rep.Entries)
}
