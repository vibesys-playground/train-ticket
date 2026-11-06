package ttoracle_test

import (
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"
	"vibesys/train-ticket-k8s-evaluator/internal/ttfake"
	"vibesys/train-ticket-k8s-evaluator/internal/ttoracle"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

func dataset(seed map[string]string) *ttoracle.Dataset {
	d := &ttoracle.Dataset{Initial: map[string][]map[string]any{}}
	for k, e := range seed {
		t, _, _ := strings.Cut(k, "/")
		var m map[string]any
		_ = json.Unmarshal([]byte(e), &m)
		d.Initial[t] = append(d.Initial[t], m)
	}
	return d
}

func run(t testing.TB, h *check.History, reinit bool) *check.Report {
	t.Helper()
	d := dataset(ttfake.Seed())
	if reinit {
		d.Reinit = d.Initial
	} else {
		d.Reinit = map[string][]map[string]any{} // restarts change nothing
	}
	spec, err := ttoracle.Spec(d)
	if err != nil {
		t.Fatal(err)
	}
	o := check.DefaultOptions
	o.TotalTimeout = 2 * time.Minute
	return check.Check(h, []check.AnySpec{spec}, o)
}

func record(p ttfake.Params, bugs ttfake.Bugs, reinit bool, wrap func(ttfake.Doer) ttfake.Doer) *check.History {
	f := ttfake.New(ttfake.Seed(), bugs)
	f.Reinit = reinit
	f.EmptyRoute = true // the real travel service's join answer for a gone route
	var d ttfake.Doer = f
	if wrap != nil {
		d = wrap(f)
	}
	phases, faults := ttfake.Record(d, p)
	return check.NewHistory(phases, faults)
}

func TestCorrect(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		r := run(t, record(ttfake.Defaults(seed), ttfake.Bugs{}, false, nil), false)
		if r.Verdict != check.Pass {
			t.Fatalf("seed %d: %s\n%s", seed, r.Verdict, r.Text(5))
		}
		if seed == 0 {
			c := r.Specs[0].Coverage
			t.Logf("seed 0: %s calls=%d pieces=%d partitions=%d elapsed=%v", r.Verdict, c.Calls, c.Pieces, len(r.Specs[0].Partitions), r.Elapsed)
		}
	}
}

// 5xx and lost responses are unknown outcomes: the oracle allows both outcomes.
func TestCorrectWithUnknownOutcomes(t *testing.T) {
	for seed := int64(0); seed < 10; seed++ {
		p := ttfake.Defaults(seed)
		p.Unknown = 0.05
		d := dataset(ttfake.Seed())
		spec, _ := ttoracle.Spec(d)
		o := check.DefaultOptions
		o.MaxLostShare = 0.2
		h := record(p, ttfake.Bugs{}, false, nil)
		r := check.Check(h, []check.AnySpec{spec}, o)
		if r.Verdict != check.Pass {
			t.Fatalf("seed %d: %s\n%s", seed, r.Verdict, r.Text(5))
		}
	}
}

// The share of unknown outcomes is capped: at the accuracy load a lost response is the candidate's fault.
func TestTooManyUnknownOutcomesFail(t *testing.T) {
	p := ttfake.Defaults(1)
	p.Unknown = 0.5
	r := run(t, record(p, ttfake.Bugs{}, false, nil), false)
	if r.Verdict != check.Fail || !r.Specs[0].LostShareExceeded {
		t.Fatalf("got %s", r.Verdict)
	}
}

// The real services re-apply their seed on restart (InitData). A model that ignores that rejects them.
func TestRestartReseed(t *testing.T) {
	for seed := int64(0); seed < 10; seed++ {
		p := ttfake.Defaults(seed)
		p.Phases = 7
		h := record(p, ttfake.Bugs{}, true, nil)
		if r := run(t, h, true); r.Verdict != check.Pass {
			t.Fatalf("seed %d: oracle with re-seed: %s\n%s", seed, r.Verdict, r.Text(5))
		}
	}
	rejected := 0
	for seed := int64(0); seed < 10; seed++ {
		p := ttfake.Defaults(seed)
		p.Phases = 7
		if run(t, record(p, ttfake.Bugs{}, true, nil), false).Verdict == check.Fail {
			rejected++
		}
	}
	t.Logf("an oracle without the re-seed rejects the re-seeding app in %d/10 histories", rejected)
	if rejected == 0 {
		t.Fatal("re-seed never mattered")
	}
}

func TestBugs(t *testing.T) {
	bugs := []struct {
		name string
		b    ttfake.Bugs
	}{
		{"lost update", ttfake.Bugs{LostUpdate: 0.3}},
		{"stale secondary index", ttfake.Bugs{StaleIndex: true}},
		{"dangling reference after delete", ttfake.Bugs{DanglingRef: true}},
		{"delete resurrects after restart", ttfake.Bugs{Resurrect: true}},
		{"wrong schema field", ttfake.Bugs{BadSchema: 0.03}},
		{"generated id collision", ttfake.Bugs{IDCollision: true}},
		{"listing drops its last element", ttfake.Bugs{ListDropsLast: true}},
	}
	for _, bg := range bugs {
		caught, n := 0, 20
		first := ""
		for seed := int64(0); seed < int64(n); seed++ {
			r := run(t, record(ttfake.Defaults(seed), bg.b, false, nil), false)
			if r.Verdict == check.Fail {
				caught++
				if first == "" {
					first = r.Text(1)
				}
			} else if r.Verdict == check.Unknown {
				t.Errorf("%s seed %d: UNKNOWN", bg.name, seed)
			}
		}
		t.Logf("%-34s caught %d/%d", bg.name, caught, n)
		if caught == 0 {
			t.Errorf("%s never caught", bg.name)
		}
	}
}

// proxy injects a bug between the checker's client and the system, like the real-deployment mutants.
type proxy struct {
	ttfake.Doer
	kind  string
	mu    sync.Mutex
	cache map[string][2]any
}

func (p *proxy) Do(in ttfake.In) (int, []byte) {
	st, b := p.Doer.Do(in)
	switch p.kind {
	case "stale": // 30% of reads replay an earlier answer to the same request
		if in.Op == "get" || in.Op == "list" || in.Op == "query" {
			k := in.Op + in.T + in.K + in.Q + in.A + in.B
			p.mu.Lock()
			defer p.mu.Unlock()
			if old, ok := p.cache[k]; ok && rand.Float64() < 0.3 {
				return old[0].(int), old[1].([]byte)
			}
			p.cache[k] = [2]any{st, b}
		}
	case "drop": // 20% of price answers lose a field
		if in.T == "price" && rand.Float64() < 0.2 {
			b = []byte(strings.Replace(string(b), `"firstClassPriceRate"`, `"firstClassPriceRat"`, 1))
		}
	}
	return st, b
}

func TestMutants(t *testing.T) {
	for _, kind := range []string{"stale", "drop"} {
		caught := 0
		for seed := int64(0); seed < 10; seed++ {
			h := record(ttfake.Defaults(seed), ttfake.Bugs{}, false, func(d ttfake.Doer) ttfake.Doer {
				return &proxy{Doer: d, kind: kind, cache: map[string][2]any{}}
			})
			if run(t, h, false).Verdict == check.Fail {
				caught++
			}
		}
		t.Logf("mutant %-6s caught %d/10", kind, caught)
		if caught < 9 {
			t.Errorf("mutant %s caught only %d/10", kind, caught)
		}
	}
}

// call builds one call of a hand-written history.
func call(info ttapi.Info, start, end int64, status int, body string) check.Call {
	raw, _ := json.Marshal(info)
	return check.Call{Op: info.OpName(), Info: raw, Start: start, End: end,
		Steps: []check.Step{{Start: start, End: end, Status: status, Body: []byte(body)}}}
}

func TestRacyCreate(t *testing.T) {
	st := ttfake.Station("x1", "Name x", 5)
	info := ttapi.Info{Table: "station", Op: "create", Entity: json.RawMessage(st)}
	created := `{"status":1,"msg":"m","data":null}`
	// two overlapping creates of one id, both answered "created": find-then-save allows it
	over := check.NewHistory([][]check.Call{{call(info, 0, 100, 201, created), call(info, 50, 150, 201, created)}}, nil)
	if r := run(t, over, false); r.Verdict != check.Pass {
		t.Fatalf("overlapping creates: %s\n%s", r.Verdict, r.Text(3))
	}
	// the same two creates one after the other: the second must have failed
	seq := check.NewHistory([][]check.Call{{call(info, 0, 100, 201, created), call(info, 200, 300, 201, created)}}, nil)
	if r := run(t, seq, false); r.Verdict != check.Fail {
		t.Fatalf("sequential creates: %s", r.Verdict)
	}
}

func TestDanglingJoin(t *testing.T) {
	rt := ttfake.Route("route-long-77", []string{"a", "b"}, []int{0, 5})
	trip := ttfake.Trip("G", "9", "TeKuai", "route-long-77", "a", "b", 1, 2)
	ok := `{"status":1,"msg":"m","data":null}`
	rtBody := `{"status":1,"msg":"m","data":` + rt + `}`
	join := ttapi.Info{Table: "travel", Op: "query", Q: "tripRoute", Key: "G9"}
	mk := func(joinBody string) *check.History {
		return check.NewHistory([][]check.Call{{
			call(ttapi.Info{Table: "route", Op: "upsert", Entity: json.RawMessage(rt)}, 0, 10, 200, rtBody),
			call(ttapi.Info{Table: "travel", Op: "create", Entity: json.RawMessage(trip)}, 20, 30, 201, ok),
			call(join, 40, 50, 200, `{"status":1,"msg":"m","data":`+ttfake.JoinRoute(rt)+`}`),
			call(ttapi.Info{Table: "route", Op: "delete", Key: "route-long-77"}, 60, 70, 200, ok),
			call(join, 80, 90, 200, joinBody),
		}}, nil)
	}
	empty := `{"status":1,"msg":"m","data":{"id":null,"stations":null,"distances":null,"startStationId":null,"terminalStationId":null}}`
	if r := run(t, mk(empty), false); r.Verdict != check.Pass {
		t.Fatalf("empty route after delete: %s\n%s", r.Verdict, r.Text(3))
	}
	stale := `{"status":1,"msg":"m","data":` + ttfake.JoinRoute(rt) + `}`
	if r := run(t, mk(stale), false); r.Verdict != check.Fail {
		t.Fatalf("a deleted route returned by the join: %s", r.Verdict)
	}
}

// Two overlapping deletes of one entity both find it and both answer "deleted" (find-then-delete);
// one after the other, the second must have found nothing.
func TestRacyDelete(t *testing.T) {
	st := ttfake.Station("beijing", "Bei", 3) // a seeded station
	del := ttapi.Info{Table: "station", Op: "delete", Key: "beijing", Entity: json.RawMessage(st)}
	deleted := `{"status":1,"msg":"m","data":null}`
	over := check.NewHistory([][]check.Call{{call(del, 0, 100, 200, deleted), call(del, 50, 150, 200, deleted)}}, nil)
	if r := run(t, over, false); r.Verdict != check.Pass {
		t.Fatalf("overlapping deletes: %s\n%s", r.Verdict, r.Text(3))
	}
	seq := check.NewHistory([][]check.Call{{call(del, 0, 100, 200, deleted), call(del, 200, 300, 200, deleted)}}, nil)
	if r := run(t, seq, false); r.Verdict != check.Fail {
		t.Fatalf("sequential deletes: %s", r.Verdict)
	}
}

// An update that found the entity before an overlapping delete removed it saves it back
// (find-then-save): a later read sees the update's value. Without the overlap it is a bug.
func TestUpdateAfterRacyDeleteSavesItBack(t *testing.T) {
	tr := ttfake.Train("TeKuai", 7, 8, 99) // a seeded train type
	ok := `{"status":1,"msg":"m","data":null}`
	upd := ttapi.Info{Table: "train", Op: "update", Entity: json.RawMessage(tr)}
	del := ttapi.Info{Table: "train", Op: "delete", Key: "TeKuai"}
	get := ttapi.Info{Table: "train", Op: "get", Key: "TeKuai"}
	body := `{"status":1,"msg":"m","data":` + tr + `}`
	mk := func(updStart int64) *check.History {
		return check.NewHistory([][]check.Call{{
			call(del, 0, 100, 200, ok),
			call(upd, updStart, updStart+100, 200, ok),
			call(get, 300, 310, 200, body),
		}}, nil)
	}
	if r := run(t, mk(50), false); r.Verdict != check.Pass {
		t.Fatalf("an update overlapping a delete saved the train back: %s\n%s", r.Verdict, r.Text(3))
	}
	if r := run(t, mk(150), false); r.Verdict != check.Fail {
		t.Fatalf("an update after the delete answered 1 and the train is back: %s", r.Verdict)
	}
}

// Lost reads count toward the lost share (a piece without keys): at the accuracy load they are the
// candidate's fault too.
func TestLostReadsCount(t *testing.T) {
	get := ttapi.Info{Table: "train", Op: "get", Key: "TeKuai"}
	var calls []check.Call
	for i := int64(0); i < 10; i++ {
		c := call(get, i*100, i*100+10, 200, `{"status":1,"msg":"m","data":`+ttfake.Train("TeKuai", 2147483647, 2147483647, 120)+`}`)
		if i < 3 {
			c.Steps[0].Status, c.Steps[0].Body = 0, nil
		}
		calls = append(calls, c)
	}
	r := run(t, check.NewHistory([][]check.Call{calls}, nil), false)
	if r.Verdict != check.Fail || !r.Specs[0].LostShareExceeded {
		t.Fatalf("3 of 10 reads lost: %s\n%s", r.Verdict, r.Text(3))
	}
}

// A restart re-seeds only the restarted service's table; a fault on another Deployment changes
// nothing.
func TestRestartTargetsItsTable(t *testing.T) {
	ok := `{"status":1,"msg":"m","data":null}`
	seeded := ttfake.Train("TeKuai", 2147483647, 2147483647, 120)
	del := ttapi.Info{Table: "train", Op: "delete", Key: "TeKuai"}
	get := ttapi.Info{Table: "train", Op: "get", Key: "TeKuai"}
	back := `{"status":1,"msg":"m","data":` + seeded + `}`
	mk := func(target string) *check.History {
		return check.NewHistory([][]check.Call{
			{call(del, 0, 10, 200, ok)},
			{call(get, 100, 110, 200, back)},
		}, []check.Fault{{Kind: "restart", Target: target, Start: 20, End: 90, AfterPhase: 0}})
	}
	if r := run(t, mk("ts-train-service"), true); r.Verdict != check.Pass {
		t.Fatalf("restarting the train service brings the seeded train back: %s\n%s", r.Verdict, r.Text(3))
	}
	if r := run(t, mk("ts-route-service"), true); r.Verdict != check.Fail {
		t.Fatalf("restarting the route service brought a train back: %s", r.Verdict)
	}
}
