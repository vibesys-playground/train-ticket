package ttaccuracy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttaccuracy"
	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"
	"vibesys/train-ticket-k8s-evaluator/internal/ttcatalog"
	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
	"vibesys/train-ticket-k8s-evaluator/internal/ttfake"
	"vibesys/train-ticket-k8s-evaluator/internal/ttoracle"
)

// fixture is the task's fixture: dataset.py's seed and size.
func fixture(t testing.TB) *ttdata.Dataset {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "..", "dataset.py"))
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) string {
		match := regexp.MustCompile(`(?m)^` + name + ` = (.+)$`).FindSubmatch(text)
		if match == nil {
			t.Fatalf("dataset.py has no %s", name)
		}
		return strings.Trim(string(match[1]), `"`)
	}
	seed, _ := strconv.ParseUint(find("SEED"), 0, 64)
	records, _ := strconv.Atoi(find("RECORDS"))
	data := ttdata.Generate(seed, records)
	if data.Digest() != find("DIGEST") {
		t.Fatal("the fixture's digest is not dataset.py's")
	}
	return data
}

type drawn struct {
	name string
	info ttapi.Info
	req  workload.Request
}

// stream draws n requests as the generator does: an operation by weight, then its Build.
func stream(t testing.TB, data *ttdata.Dataset, seed uint64, n int) []drawn {
	t.Helper()
	w := ttaccuracy.Workload()
	m, err := w.Model(data, workload.NewRand(seed, "model"))
	if err != nil {
		t.Fatal(err)
	}
	weights := make([]float64, len(w.Operations))
	for i, op := range w.Operations {
		weights[i] = op.Weight
	}
	r := workload.NewRand(seed, "phase")
	out := make([]drawn, n)
	for i := range out {
		op := w.Operations[r.Weighted(weights)]
		req := op.Build(m, r.Child("op", i))
		out[i] = drawn{op.Name, req.Info.(ttapi.Info), req}
	}
	return out
}

// TestEveryRequestIsTheOraclesOperation: every request is one step of the API call its Info maps
// to, under the Info's operation name, and carries a valid entity.
func TestEveryRequestIsTheOraclesOperation(t *testing.T) {
	data := fixture(t)
	counts := map[string]int{}
	writes, lists, generated := 0, 0, 0
	const n = 20000
	for i, d := range stream(t, data, 7, n) {
		counts[d.name]++
		if d.name != d.info.OpName() {
			t.Fatalf("request %d: operation %s, info %s", i, d.name, d.info.OpName())
		}
		method, path, _, err := ttapi.Request(d.info)
		if err != nil {
			t.Fatalf("request %d (%s): %v", i, d.name, err)
		}
		if len(d.req.Steps) != 1 || d.req.Steps[0].String() != "Step("+method+" "+path+")" {
			t.Fatalf("request %d (%s): steps %v, want %s %s", i, d.name, d.req.Steps, method, path)
		}
		if len(d.info.Entity) > 0 {
			if _, _, err := ttapi.Canon(d.info.Table, d.info.Entity); err != nil {
				t.Fatalf("request %d (%s): %v", i, d.name, err)
			}
			if d.info.Generated(string(d.info.Entity)) {
				generated++
			}
		}
		switch d.info.Op {
		case "create", "update", "upsert", "delete":
			writes++
		case "list":
			lists++
		}
	}
	if len(counts) != 34 {
		t.Errorf("%d operations drawn, want 34: %v", len(counts), counts)
	}
	t.Logf("writes %.3f, lists %.3f, generated ids %.4f of %d", float64(writes)/n, float64(lists)/n, float64(generated)/n, n)
	if share := float64(writes) / n; share < 0.4 || share > 0.5 {
		t.Errorf("writes are %.3f of the stream", share)
	}
	if share := float64(generated) / n; share > 0.01 {
		t.Errorf("generated-id upserts are %.4f of the stream: each makes a key listings carry to the end", share)
	}
}

func seedOf(t testing.TB, d *ttoracle.Dataset) map[string]string {
	t.Helper()
	out := map[string]string{}
	for table, list := range d.Initial {
		for _, e := range list {
			raw, _ := json.Marshal(e)
			k, c, err := ttapi.Canon(table, raw)
			if err != nil {
				t.Fatal(err)
			}
			out[k] = c
		}
	}
	return out
}

func catalog(t testing.TB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for table, list := range ttcatalog.Entities() {
		for _, raw := range list {
			k, c, err := ttapi.Canon(table, raw)
			if err != nil {
				t.Fatal(err)
			}
			out[k] = c
		}
	}
	return out
}

// faults is the accuracy check's schedule (accuracy.py): after phase i, restart these services.
var faults = [][]string{{"route", "price"}, {"station", "config"}, {"train", "travel"}}

// record runs the workload against the fake with clients concurrent clients per phase, and the
// fault schedule between phases.
func record(t testing.TB, data *ttdata.Dataset, bugs ttfake.Bugs, seed uint64, clients, perClient int) *check.History {
	t.Helper()
	d, err := ttoracle.ForFixture(data)
	if err != nil {
		t.Fatal(err)
	}
	f := ttfake.New(seedOf(t, d), bugs)
	f.Reinit, f.ReinitSeed, f.EmptyRoute = true, catalog(t), true
	ops := stream(t, data, seed, (len(faults)+1)*clients*perClient)
	var phases [][]check.Call
	var recorded []check.Fault
	next := 0
	for phase := 0; phase <= len(faults); phase++ {
		var mu sync.Mutex
		var calls []check.Call
		var wg sync.WaitGroup
		for c := range clients {
			mine := ops[next : next+perClient]
			next += perClient
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, d := range mine {
					start := time.Now().UnixNano()
					status, body := f.DoInfo(d.info)
					end := time.Now().UnixNano()
					raw, _ := json.Marshal(d.info)
					call := check.Call{Phase: phase, Client: c, Op: d.name, Info: raw, Start: start, End: end,
						Steps: []check.Step{{Start: start, End: end, Status: status, Body: body}}}
					mu.Lock()
					calls = append(calls, call)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		sort.Slice(calls, func(i, j int) bool { return calls[i].Start < calls[j].Start })
		for i := range calls {
			calls[i].Index = uint64(i)
		}
		phases = append(phases, calls)
		if phase < len(faults) {
			for _, table := range faults[phase] {
				start := time.Now().UnixNano()
				kind, target := f.RestartService(table)
				recorded = append(recorded, check.Fault{Kind: kind, Target: target, Start: start, End: time.Now().UnixNano(), AfterPhase: phase})
			}
		}
	}
	return check.NewHistory(phases, recorded)
}

func verdict(t testing.TB, data *ttdata.Dataset, h *check.History) *check.Report {
	t.Helper()
	d, err := ttoracle.ForFixture(data)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ttoracle.Spec(d)
	if err != nil {
		t.Fatal(err)
	}
	o := check.DefaultOptions
	o.TotalTimeout = 2 * time.Minute
	return check.Check(h, []check.AnySpec{spec}, o)
}

// TestTheOracleAcceptsTheWorkloadOnACorrectApplication: the workload's requests against the
// in-memory fake (seeded with the catalog and the fixture, re-seeding the catalog on restart),
// with the check's restarts between phases, make a history the oracle accepts.
func TestTheOracleAcceptsTheWorkloadOnACorrectApplication(t *testing.T) {
	data := fixture(t)
	for seed := uint64(1); seed <= 3; seed++ {
		r := verdict(t, data, record(t, data, ttfake.Bugs{}, seed, 8, 400))
		c := r.Specs[0].Coverage
		t.Logf("seed %d: %s, %d calls, %d pieces, %d partitions, most in flight %d, check %v", seed, r.Verdict,
			c.Calls, c.Pieces, len(r.Specs[0].Partitions), r.Specs[0].MaxInFlight, r.Elapsed)
		if r.Verdict != check.Pass {
			t.Fatalf("seed %d: %s\n%s", seed, r.Verdict, r.Text(5))
		}
	}
}

// TestTheOracleRejectsABrokenApplication: the same workload against a fake with each injected bug
// makes a history the oracle fails.
func TestTheOracleRejectsABrokenApplication(t *testing.T) {
	data := fixture(t)
	for _, bug := range []struct {
		name string
		bugs ttfake.Bugs
	}{
		{"lost update", ttfake.Bugs{LostUpdate: 0.3}},
		{"stale secondary index", ttfake.Bugs{StaleIndex: true}},
		{"delete resurrects after restart", ttfake.Bugs{Resurrect: true}},
		{"wrong schema field", ttfake.Bugs{BadSchema: 0.03}},
		{"listing drops its last element", ttfake.Bugs{ListDropsLast: true}},
	} {
		r := verdict(t, data, record(t, data, bug.bugs, 1, 8, 400))
		t.Logf("%-32s %s", bug.name, r.Verdict)
		if r.Verdict != check.Fail {
			t.Errorf("%s: %s, want FAIL\n%s", bug.name, r.Verdict, r.Text(3))
		}
	}
}

func TestExtraIDsAreWhatTheServicesKeep(t *testing.T) {
	seen := map[string]bool{}
	for _, table := range ttapi.Tables {
		for i := range ttaccuracy.Extra {
			id := ttaccuracy.ExtraID(table, i)
			// a route's or price's id shorter than 10 makes the service generate one
			if (table == "route" || table == "price") && len(id) < 10 || seen[table+id] {
				t.Fatalf("%s id %q", table, id)
			}
			seen[table+id] = true
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(ttaccuracy.ExtraID("price", 3)) {
		t.Fatal("a price id is a UUID")
	}
	if id := ttaccuracy.ExtraID("travel", 3); !regexp.MustCompile(`^[GD][0-9]+$`).MatchString(id) {
		t.Fatalf("trip id %q", id)
	}
}
