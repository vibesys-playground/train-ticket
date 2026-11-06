// Package ttoracle is the Train Ticket accuracy oracle: the semantics of the six services as plain
// Go over per-entity state, for vibesys-evaluator's model-based checker (go/check).
//
// One key per entity ("station/shanghai"). A listing or a secondary-index query is projected into
// one piece per key it could have returned (per-key views, no whole-collection snapshots: Mongo's
// findAll is not one). The model is the application as it is, not as a contract might read:
//
//   - A restart re-applies the service's seed data (InitData): create-if-absent for config,
//     station, train and trip, an upsert (overwrite) for route and price.
//   - config/station/train create is find-then-save, so two overlapping creates of one id can
//     both answer "created"; the later save wins.
//   - Route and price POST are upserts, and the server generates the id when the request's is
//     missing or shorter than 10 characters.
//   - Route DELETE removes, then re-reads: it answers status 0 if a concurrent upsert brought the
//     route back, so a status 0 on a present route is a no-op.
//   - Duplicate trip create and update of a missing trip both answer status 1 and change nothing.
//   - GET /travelservice/routes/{trip} answers an empty route (status 1) when the trip's route is
//     gone, and carries only the id, stations and distances of the route.
package ttoracle

import (
	"fmt"
	"strings"
	"time"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

// Kind of piece: what the piece asserts about its one key.
const (
	kCreate    = "create"
	kUpdate    = "update"
	kUpsert    = "upsert"    // known id
	kGenUpsert = "genupsert" // server-generated id: the key is the response's
	kDelete    = "delete"
	kGet       = "get"
	kMember    = "member" // in/not in a listing or a query result (Q "" = listing)
	kName      = "name"   // station name of an id
	kJoinTrip  = "jointrip"
	kJoinRef   = "joinref"
)

// In is a piece's input.
type In struct {
	Kind    string
	Table   string
	Q, A, B string
	E       string // canonical entity a write carries
}

// Out is a piece's output.
type Out struct {
	Status  int
	Present bool   // get/member/join: the answer has this key's entity
	Ent     string // canonical entity (""= not compared)
	Val     string // station name; a join's referenced id
	Empty   bool   // join: the route is gone (empty route)
}

type rec struct{ Call, Ret int64 }

// State is one entity's state. Recent remembers the creates that took effect lately, for the
// racy-create rule; it is pruned at Op.Floor.
type State struct {
	Key    string
	Ent    string // canonical entity; "" = absent
	Recent []rec
}

func (s State) with(ent string) State { s.Ent = ent; return s }

func (s State) created(call, ret, floor int64) State {
	keep := make([]rec, 0, len(s.Recent)+1)
	for _, r := range s.Recent {
		if r.Ret >= floor {
			keep = append(keep, r)
		}
	}
	s.Recent = append(keep, rec{call, ret})
	return s
}

func (s State) pruned(floor int64) State {
	if len(s.Recent) == 0 {
		return s
	}
	keep := make([]rec, 0, len(s.Recent))
	for _, r := range s.Recent {
		if r.Ret >= floor {
			keep = append(keep, r)
		}
	}
	s.Recent = keep
	return s
}

func (s State) overlapsCreate(call, ret int64) bool {
	for _, r := range s.Recent {
		if r.Call < ret && call < r.Ret {
			return true
		}
	}
	return false
}

func equal(a, b State) bool {
	if a.Key != b.Key || a.Ent != b.Ent || len(a.Recent) != len(b.Recent) {
		return false
	}
	for i := range a.Recent {
		if a.Recent[i] != b.Recent[i] {
			return false
		}
	}
	return true
}

func hash(s State) uint64 {
	h := uint64(14695981039346656037)
	mix := func(b []byte) {
		for _, c := range b {
			h = (h ^ uint64(c)) * 1099511628211
		}
	}
	mix([]byte(s.Key))
	mix([]byte{0})
	mix([]byte(s.Ent))
	for _, r := range s.Recent {
		h = (h ^ uint64(r.Call)) * 1099511628211
		h = (h ^ uint64(r.Ret)) * 1099511628211
	}
	return h
}

// Dataset is what the oracle needs of the dataset file: the entities present when the history
// starts (the seeded fixture), and the entities a service's startup re-applies (InitData;
// Reinit defaults to Initial). Both have the shape of seed_catalog.json: table -> entities.
type Dataset struct {
	Initial map[string][]map[string]any `json:"initial"`
	Reinit  map[string][]map[string]any `json:"reinit"`
}

type seed struct {
	initial map[string]string // key -> canonical entity
	reinit  map[string]string
}

// effectWindow: how long a write whose response was lost can still take effect (loadgen's step timeout).
const effectWindow = 10 * time.Second

// Spec builds the oracle's spec for a dataset.
func Spec(d *Dataset) (*check.Spec[State, In, Out], error) {
	sd, err := loadSeed(d)
	if err != nil {
		return nil, err
	}
	m := &model{seed: sd}
	return &check.Spec[State, In, Out]{
		Name:         "train-ticket",
		Project:      m.project,
		Init:         func(keys []string) State { return State{Key: keys[0], Ent: sd.initial[keys[0]]} },
		Step:         m.step,
		Fault:        m.fault,
		Equal:        equal,
		Hash:         hash,
		EffectWindow: effectWindow,
		DescribeState: func(s State) string {
			if s.Ent == "" {
				return s.Key + " absent"
			}
			return s.Key + " = " + s.Ent
		},
		DescribeOp: func(op check.Op[In, Out]) string {
			in := op.In
			b := &strings.Builder{}
			fmt.Fprintf(b, "%s %s", in.Kind, in.Table)
			if in.Q != "" {
				fmt.Fprintf(b, " q=%s(%s,%s)", in.Q, in.A, in.B)
			}
			if in.E != "" {
				fmt.Fprintf(b, " %s", in.E)
			}
			if op.Unknown {
				return b.String() + " -> ???"
			}
			fmt.Fprintf(b, " -> status=%d present=%v", op.Out.Status, op.Out.Present)
			if op.Out.Ent != "" {
				fmt.Fprintf(b, " ent=%s", op.Out.Ent)
			}
			if op.Out.Val != "" {
				fmt.Fprintf(b, " val=%s", op.Out.Val)
			}
			return b.String()
		},
	}, nil
}

func loadSeed(d *Dataset) (*seed, error) {
	s := &seed{initial: map[string]string{}, reinit: map[string]string{}}
	load := func(dst map[string]string, src map[string][]map[string]any) error {
		for t, list := range src {
			for _, e := range list {
				raw, err := marshal(e)
				if err != nil {
					return err
				}
				k, c, err := ttapi.Canon(t, raw)
				if err != nil {
					return fmt.Errorf("dataset %s: %w", t, err)
				}
				dst[k] = c
			}
		}
		return nil
	}
	if err := load(s.initial, d.Initial); err != nil {
		return nil, err
	}
	if d.Reinit == nil {
		for k, v := range s.initial {
			s.reinit[k] = v
		}
	} else if err := load(s.reinit, d.Reinit); err != nil {
		return nil, err
	}
	return s, nil
}

type model struct{ seed *seed }

func tableOf(key string) string { t, _, _ := strings.Cut(key, "/"); return t }

type cfg struct{ dup, updMiss, delMiss int }

var cfgs = map[string]cfg{
	"config": {0, 0, 0}, "station": {0, 0, 0}, "train": {0, 0, 0},
	"route": {0, 0, 1}, "price": {0, 0, 0}, "travel": {1, 1, 0},
}

// fault: a restart of a service re-applies its seed. Target is the service ("station",
// "ts-station-service") or "" / "all" for every service (restart_all).
func (m *model) fault(s State, f check.Fault) []State {
	if f.Kind != "restart" && f.Kind != "restart_all" {
		return []State{s}
	}
	t := tableOf(s.Key)
	if f.Kind == "restart" {
		target := strings.TrimSuffix(strings.TrimPrefix(f.Target, "ts-"), "-service")
		if target != t {
			return []State{s}
		}
	}
	e, ok := m.seed.reinit[s.Key]
	if !ok {
		return []State{s}
	}
	if s.Ent == "" || t == "route" || t == "price" { // InitData: create-if-absent, upsert for route and price
		s.Ent = e
	}
	return []State{s}
}

// write applies the effect of a write that took effect. ok=false: its precondition failed (no-op).
func write(s State, in In, op check.Op[In, Out]) (State, bool) {
	switch in.Kind {
	case kCreate:
		if s.Ent != "" {
			return s, false
		}
		return s.with(in.E).created(op.Call, op.Return, op.Floor), true
	case kUpdate:
		if s.Ent == "" {
			return s, false
		}
		return s.with(in.E), true
	case kUpsert:
		return s.with(in.E), true
	case kDelete:
		if s.Ent == "" {
			return s, false
		}
		return s.with(""), true
	}
	return s, false
}

func (m *model) step(s State, op check.Op[In, Out]) ([]State, string) {
	in, out := op.In, op.Out
	s = s.pruned(op.Floor)
	one := func(n State) ([]State, string) { return []State{n}, "" }
	switch in.Kind {
	case kGenUpsert:
		id := strings.TrimPrefix(s.Key, in.Table+"/")
		gen := ttapi.WithID(in.E, id)
		if op.Unknown { // a lost generate-id upsert may have created this (orphan) key
			if s.Ent != "" {
				return one(s)
			}
			return []State{s, s.with(gen)}, ""
		}
		if s.Ent != "" {
			return nil, fmt.Sprintf("the server generated %s, which already exists", s.Key)
		}
		if out.Status != 1 || out.Ent != gen {
			return nil, fmt.Sprintf("generated-id upsert answered status %d entity %s, want %s", out.Status, out.Ent, gen)
		}
		return one(s.with(gen))
	case kCreate, kUpdate, kUpsert, kDelete:
		n, ok := write(s, in, op)
		if op.Unknown {
			if ok {
				return []State{s, n}, ""
			}
			if in.Kind == kDelete && in.Table == "route" {
				return []State{s, s.with("")}, ""
			}
			return one(s)
		}
		cf := cfgs[in.Table]
		want := 1
		if !ok {
			switch in.Kind {
			case kCreate:
				want = cf.dup
				// find-then-save: an overlapping create may also have found the id absent
				if out.Status == 1 && cf.dup == 0 && s.overlapsCreate(op.Call, op.Return) {
					a := s.created(op.Call, op.Return, op.Floor)
					b := a.with(in.E)
					return []State{a, b}, ""
				}
			case kUpdate:
				want = cf.updMiss
			case kDelete:
				want = cf.delMiss
			}
		}
		if in.Kind == kDelete && in.Table == "route" {
			// removeById then findById: status 0 when a concurrent upsert brought the route back
			if out.Status == 0 && s.Ent != "" {
				return one(s)
			}
			want, n = 1, s.with("")
		}
		if out.Status != want {
			return nil, fmt.Sprintf("%s %s answered status %d; with %s the answer is %d", in.Kind, s.Key, out.Status, present(s), want)
		}
		if in.Kind == kUpsert && out.Ent != in.E {
			return nil, fmt.Sprintf("upsert echoed %s, sent %s", out.Ent, in.E)
		}
		return one(n)
	case kGet:
		if s.Ent == "" {
			if out.Present {
				return nil, fmt.Sprintf("%s is absent but the read returned %s", s.Key, out.Ent)
			}
			return one(s)
		}
		if !out.Present {
			return nil, fmt.Sprintf("%s exists (%s) but the read found nothing", s.Key, s.Ent)
		}
		if out.Ent != s.Ent {
			return nil, fmt.Sprintf("read %s, the model has %s", out.Ent, s.Ent)
		}
		return one(s)
	case kName:
		if s.Ent == "" {
			if out.Present {
				return nil, fmt.Sprintf("%s is absent but its name was answered %q", s.Key, out.Val)
			}
			return one(s)
		}
		if !out.Present || out.Val != ttapi.StrField(s.Ent, "name") {
			return nil, fmt.Sprintf("station name read %q present=%v, the model has %s", out.Val, out.Present, s.Ent)
		}
		return one(s)
	case kMember:
		match := s.Ent != "" && ttapi.Matches(in.Q, in.A, in.B, s.Ent)
		if out.Present != match {
			if out.Present {
				return nil, fmt.Sprintf("%s is in the answer but %s", s.Key, why(s, in))
			}
			return nil, fmt.Sprintf("%s is missing from the answer, yet it %s", s.Key, "is "+s.Ent)
		}
		if match && out.Ent != "" && out.Ent != s.Ent {
			return nil, fmt.Sprintf("answer has %s, the model has %s", out.Ent, s.Ent)
		}
		return one(s)
	case kJoinTrip:
		ref := "routeId"
		if in.Q == "tripTrain" {
			ref = "trainTypeId"
		}
		if !out.Present { // tripRoute status 0: no such trip
			if s.Ent != "" {
				return nil, fmt.Sprintf("trip %s exists, the join answered nothing", s.Key)
			}
			return one(s)
		}
		if s.Ent == "" {
			return nil, fmt.Sprintf("trip %s is absent but the join answered", s.Key)
		}
		if !out.Empty && ttapi.StrField(s.Ent, ref) != out.Val {
			return nil, fmt.Sprintf("join answered %s %q, the trip has %q", ref, out.Val, ttapi.StrField(s.Ent, ref))
		}
		return one(s)
	case kJoinRef:
		cur := s.Ent
		if cur != "" && in.Q == "tripRoute" {
			cur = ttapi.ProjRoute(cur)
		}
		if cur == "" {
			return nil, fmt.Sprintf("%s is absent but a join returned %s (dangling reference)", s.Key, out.Ent)
		}
		if cur != out.Ent {
			return nil, fmt.Sprintf("join returned %s, the model has %s", out.Ent, cur)
		}
		return one(s)
	}
	return nil, "unknown piece kind " + in.Kind
}

func present(s State) string {
	if s.Ent == "" {
		return "the entity absent"
	}
	return "the entity present"
}

func why(s State, in In) string {
	if s.Ent == "" {
		return "it is absent"
	}
	return "it does not match (" + s.Ent + ")"
}
