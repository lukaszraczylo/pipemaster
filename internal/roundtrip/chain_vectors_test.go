package roundtrip_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/report"
)

// maxChainVectorBytes bounds chain.json; the generator caps the pipelines per triple so nine adapters fit.
const maxChainVectorBytes = 3_000_000

// chainHops is the number of conversions after the origin in every vector chain.
const chainHops = 2

// TestChainVectors runs Hop over real files for every three-provider chain and compares each stage with the
// Lean model (chain_roundtrip): the IR after each parse with no metadata applied, then the unwound IR.
func TestChainVectors(t *testing.T) {
	cases := vectors.Load(t, "chain")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	seen := map[string]bool{}
	providers := map[string]bool{}
	for i, c := range cases {
		var in struct {
			Core  json.RawMessage `json:"core"`
			Chain []string        `json:"chain"`
		}
		var out struct {
			Final  json.RawMessage   `json:"final"`
			Stages []json.RawMessage `json:"stages"`
		}
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		if len(in.Chain) != chainHops+1 || len(out.Stages) != chainHops {
			t.Fatalf("case %d: chain %v with %d stages", i, in.Chain, len(out.Stages))
		}
		seen[fmt.Sprint(in.Chain)] = true
		for _, id := range in.Chain {
			providers[id] = true
		}
		label := fmt.Sprintf("case %d %v", i, in.Chain)
		runChain(t, label, in.Chain, in.Core, out.Stages, out.Final)
		if t.Failed() {
			return
		}
	}
	if want := distinctChains(len(providers)); len(seen) != want {
		t.Errorf("vectors cover %d chains over %d providers, want %d", len(seen), len(providers), want)
	}
}

// distinctChains counts the chains A to B to C to A over n providers: A, B and C pairwise differ.
func distinctChains(n int) int {
	if n < chainHops+1 {
		return 0
	}
	count := 1
	for k := 0; k <= chainHops; k++ {
		count *= n - k
	}
	return count
}

func runChain(t *testing.T, label string, chain []string, core json.RawMessage, stages []json.RawMessage, final json.RawMessage) {
	t.Helper()
	cur, err := ir.FromCore(core)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	cur.Origin.Provider, cur.Origin.Dialect = chain[0], chain[0]
	stack := &meta.Stack{}
	srcFS := toFS(nil)
	rep := &report.Report{}
	for k := 1; k <= chainHops; k++ {
		target := prov(t, chain[k])
		res, err := roundtrip.Hop(cur, stack, srcFS, chain[k-1], chain[k], false, rep)
		if err != nil || res.Restored {
			t.Fatalf("%s: hop to %s: restored=%v err=%v", label, chain[k], res.Restored, err)
		}
		files, emitRep, err := target.Emit(res.Proj)
		if err != nil {
			t.Fatalf("%s: emit %s: %v", label, chain[k], err)
		}
		m := roundtrip.NewMeta(chain[k-1], res.Proj, target, emitRep)
		if err := roundtrip.RecordEmitted(m, target, files); err != nil {
			t.Fatalf("%s: record %s: %v", label, chain[k], err)
		}
		res.Out.Layers = append(res.Out.Layers, m)
		files = meta.AttachStack(files, res.Out)
		srcFS = toFS(files)
		if cur, _, err = target.Parse(srcFS); err != nil {
			t.Fatalf("%s: parse %s: %v", label, chain[k], err)
		}
		got, err := ir.CoreOf(cur)
		if err != nil {
			t.Fatalf("%s: stage %d: %v", label, k, err)
		}
		vectors.DiffCore(t, fmt.Sprintf("%s stage %d", label, k), stages[k-1], got)
		if stack, err = meta.LoadStack(srcFS, cur.Origin.Files); err != nil {
			t.Fatalf("%s: load stack: %v", label, err)
		}
	}
	res, err := roundtrip.Hop(cur, stack, srcFS, chain[chainHops], chain[0], false, rep)
	if err != nil || !res.Restored {
		t.Fatalf("%s: return to %s: restored=%v err=%v", label, chain[0], res.Restored, err)
	}
	back, err := ir.CoreOf(res.Proj)
	if err != nil {
		t.Fatalf("%s: final: %v", label, err)
	}
	vectors.DiffCore(t, label+" final", final, back)
	vectors.DiffCore(t, label+" final equals the origin", core, back)
}

func TestDistinctChains(t *testing.T) {
	for n, want := range map[int]int{0: 0, 2: 0, 3: 6, 4: 24, 5: 60, 9: 504} {
		if got := distinctChains(n); got != want {
			t.Errorf("distinctChains(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestChainVectorsSizeBound(t *testing.T) {
	info, err := os.Stat(filepath.Join("..", "..", "testdata", "lean-vectors", "chain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxChainVectorBytes {
		t.Errorf("chain.json is %d bytes, want at most %d; lower chainCaseCap in lean4/GenVectors.lean", info.Size(), maxChainVectorBytes)
	}
}
