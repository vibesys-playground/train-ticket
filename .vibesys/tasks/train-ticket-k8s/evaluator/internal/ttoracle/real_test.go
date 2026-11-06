package ttoracle_test

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"
	"vibesys/train-ticket-k8s-evaluator/internal/ttoracle"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

// rawCapture is a history recorded against a real Train Ticket deployment (the six services on
// kind, through the gateway; 6 clients, 3 phases, a rolling restart of one service between phases)
// by the spike's client, before the accuracy flow existed: every request, status and body.
type rawCapture struct {
	Dataset struct {
		Initial map[string][]map[string]any `json:"initial"`
	} `json:"dataset"`
	Calls []struct {
		Phase      int             `json:"phase"`
		Info       json.RawMessage `json:"info"`
		Start, End int64
		Status     int    `json:"status"`
		Body       string `json:"body"`
	} `json:"calls"`
	Faults []struct {
		Kind, Target string
		Start, End   int64
		AfterPhase   int
	} `json:"faults"`
	Phases int `json:"phases"`
}

func loadCapture(t testing.TB, path string) (*ttoracle.Dataset, *check.History) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var rc rawCapture
	if err := json.NewDecoder(gz).Decode(&rc); err != nil {
		t.Fatal(err)
	}
	phases := make([][]check.Call, rc.Phases)
	for _, c := range rc.Calls {
		var info ttapi.Info
		if err := json.Unmarshal(c.Info, &info); err != nil {
			t.Fatal(err)
		}
		phases[c.Phase] = append(phases[c.Phase], check.Call{Phase: c.Phase, Client: -1, Op: info.OpName(), Info: c.Info, Start: c.Start, End: c.End,
			Steps: []check.Step{{Start: c.Start, End: c.End, Status: c.Status, Body: []byte(c.Body)}}})
	}
	for i := range phases {
		sort.Slice(phases[i], func(a, b int) bool { return phases[i][a].Start < phases[i][b].Start })
		for j := range phases[i] {
			phases[i][j].Index = uint64(j)
		}
	}
	var faults []check.Fault
	for _, f := range rc.Faults {
		faults = append(faults, check.Fault{Kind: f.Kind, Target: f.Target, Start: f.Start, End: f.End, AfterPhase: f.AfterPhase})
	}
	return &ttoracle.Dataset{Initial: rc.Dataset.Initial}, check.NewHistory(phases, faults)
}

// TestRealCapture checks, offline, a history captured from the real application: it passes.
func TestRealCapture(t *testing.T) {
	d, h := loadCapture(t, "testdata/real-capture.json.gz")
	spec, err := ttoracle.Spec(d)
	if err != nil {
		t.Fatal(err)
	}
	r := check.Check(h, []check.AnySpec{spec}, check.DefaultOptions)
	t.Logf("real capture: %s, %d calls, %d faults, %d pieces, %d partitions, check %v", r.Verdict,
		r.Specs[0].Coverage.Calls, len(h.Faults), r.Specs[0].Coverage.Pieces, len(r.Specs[0].Partitions), r.Elapsed)
	if r.Verdict != check.Pass {
		t.Fatalf("%s\n%s", r.Verdict, r.Text(5))
	}
}

// The same real history with one answer tampered with (a price entity loses a field) fails.
func TestRealCaptureTampered(t *testing.T) {
	d, h := loadCapture(t, "testdata/real-capture.json.gz")
	tampered := 0
	var phases [][]check.Call
	for i := 0; i < h.Phases; i++ {
		calls, err := h.Phase(i)
		if err != nil {
			t.Fatal(err)
		}
		for j := range calls {
			c := &calls[j]
			if tampered == 0 && c.Op == "price.list" && len(c.Steps[0].Body) > 0 {
				c.Steps[0].Body = []byte(strings.Replace(string(c.Steps[0].Body), `"basicPriceRate"`, `"basicPriceRat"`, 1))
				tampered++
			}
		}
		phases = append(phases, calls)
	}
	if tampered == 0 {
		t.Fatal("no price listing in the capture")
	}
	spec, _ := ttoracle.Spec(d)
	r := check.Check(check.NewHistory(phases, h.Faults), []check.AnySpec{spec}, check.DefaultOptions)
	if r.Verdict != check.Fail {
		t.Fatalf("tampered real history: %s", r.Verdict)
	}
}
