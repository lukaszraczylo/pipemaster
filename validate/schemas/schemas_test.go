package schemas_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

// plainDigests holds the SHA-256 of each generated schema as it was stored uncompressed.
var plainDigests = []struct{ path, sha string }{
	{"argo/clusterworkflowtemplate_v1alpha1.json", "d65a2a2bc08500f136821f031df8b2f4bafd35c1c7105c0a15df86ea7b14bdf6"},
	{"argo/cronworkflow_v1alpha1.json", "90e36f74bfcb5862081203ec02ef7e191928aea50ea4af529f004ea0eb4293ed"},
	{"argo/dispatch.json", "dadc3da1144b03dc821141e6c9b986d0252598ac1e1a7c5fc3f81f72fc88f706"},
	{"argo/eventsource_v1alpha1.json", "226fc910586c36fb7cea4fcdbd5ac60e3715d0fdc849edbe5875232b588df354"},
	{"argo/sensor_v1alpha1.json", "226fc910586c36fb7cea4fcdbd5ac60e3715d0fdc849edbe5875232b588df354"},
	{"argo/workflow_v1alpha1.json", "6dc457258ac10c9bb8c9903ac9e566c919b99feaa0d13bb91f085fd07f5ee231"},
	{"argo/workflowtemplate_v1alpha1.json", "bc9b2c61354fac39039165024d5152b52fb23cadc657c60c298848466bdfb68d"},
	{"tekton/clusterinterceptor_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/clustertriggerbinding_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/clustertriggerbinding_v1beta1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/cronjob-batch-v1.json", "fb921edacc3ded080ca322233197727f8c110254ba3514e6faade5fa06513ae4"},
	{"tekton/dispatch.json", "9dbbaed2cf15865b46e75e9a3dcd5ec2f5821c6ac297bbd49485c176eef2064e"},
	{"tekton/eventlistener_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/eventlistener_v1beta1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/pipeline_v1.json", "93d57ee930072bb3a30b1762395e25adc848a009f49eab914f64e7fef019b4b4"},
	{"tekton/pipeline_v1beta1.json", "f5e7d52f2e1933d1acb754564c49468009c7b4f055e30a5a22f30ab537665fc8"},
	{"tekton/pipelinerun_v1.json", "cc39c9544a78e13cbf15364990d92524b2b409f74bfe0f19d58a442c280a2ca0"},
	{"tekton/pipelinerun_v1beta1.json", "a8c166cc762a377c12012627064d5afec75cbe721bfa00cfdf886b3875a3469c"},
	{"tekton/stepaction_v1alpha1.json", "3f7a2dddeb612081152f937338adcbd3d2dbcda7f756cd52f7ba4720c1560efd"},
	{"tekton/stepaction_v1beta1.json", "fe56aec798012a93a1992cfb9a1b6e92d4000aea550b8e3afaee3968470057f3"},
	{"tekton/task_v1.json", "57a7949fa5e84f76e830d96e21158f0d133f02c28ee62268feb149ae33871f6f"},
	{"tekton/task_v1beta1.json", "24365940af446b79c9e6aa17f20e25da15285f34fd36929dd58b733c7c1c1d68"},
	{"tekton/taskrun_v1.json", "763d2269ca53e02a2b556644e530c0bfe2881a3f6c737e461c1f5d4e5074afa5"},
	{"tekton/taskrun_v1beta1.json", "5c10deaaa69eb0709a45066438fc448227e87c50088b2d77a08f0d0a53fbc88a"},
	{"tekton/trigger_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/trigger_v1beta1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/triggerbinding_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/triggerbinding_v1beta1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/triggertemplate_v1alpha1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"tekton/triggertemplate_v1beta1.json", "f70acc29b45b5c1a1551768b90447e462a86891b34c6691c63a3543e0cb668f0"},
	{"kubernetes-definitions.json", "e12ff558a488df7fb521815e41c01b8e1e9bf740fe7dd77bce38ae546fc460a1"},
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	switch {
	case path == "kubernetes-definitions.json":
		return schemas.KubernetesDefinitions()
	case strings.HasPrefix(path, "argo/"):
		b, err := fs.ReadFile(schemas.Argo, path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	default:
		b, err := fs.ReadFile(schemas.Tekton, path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

func TestCompressedSchemasMatchPlainDigests(t *testing.T) {
	for _, d := range plainDigests {
		sum := sha256.Sum256(read(t, d.path))
		if got := hex.EncodeToString(sum[:]); got != d.sha {
			t.Errorf("%s: sha256 %s, want %s", d.path, got, d.sha)
		}
	}
}

func TestSchemaFSListsJSONNames(t *testing.T) {
	for _, dir := range []struct {
		fsys fs.FS
		name string
	}{{schemas.Argo, "argo"}, {schemas.Tekton, "tekton"}} {
		entries, err := fs.ReadDir(dir.fsys, dir.name)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") || e.IsDir() {
				t.Errorf("%s: unexpected entry %q", dir.name, e.Name())
			}
			got[dir.name+"/"+e.Name()] = true
		}
		want := 0
		for _, d := range plainDigests {
			if strings.HasPrefix(d.path, dir.name+"/") {
				want++
				if !got[d.path] {
					t.Errorf("%s missing from listing", d.path)
				}
			}
		}
		if len(got) != want {
			t.Errorf("%s lists %d names, want %d", dir.name, len(got), want)
		}
	}
}

func TestSchemaFSContract(t *testing.T) {
	for _, dir := range []struct {
		fsys fs.FS
		name string
	}{{schemas.Argo, "argo"}, {schemas.Tekton, "tekton"}} {
		t.Run(dir.name, func(t *testing.T) {
			entries, err := fs.ReadDir(dir.fsys, dir.name)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = dir.name + "/" + e.Name()
			}
			if err := fstest.TestFS(dir.fsys, names...); err != nil {
				t.Fatal(err)
			}
		})
	}
}
