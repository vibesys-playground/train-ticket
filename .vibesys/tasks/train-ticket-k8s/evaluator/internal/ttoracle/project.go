package ttoracle

import (
	"encoding/json"
	"fmt"
	"iter"
	"sort"
	"strings"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

// deferred is a piece that needs the whole history's keys (a listing, an index query that found
// nothing, a lost generate-id upsert): it is added after the loop, one piece per candidate key.
type deferred struct {
	kind  string // list | set | none | ghost
	info  ttapi.Info
	table string
	ent   string            // ghost: the request's entity
	out   map[string]string // list/set: key -> canonical entity in the answer
	call  *check.Call       // copy without bodies
}

type universe struct {
	versions map[string]map[string]bool // key -> every canonical version the seed or the history mentions
	creators map[string]bool            // keys some write or the seed brings into being
}

func (u *universe) touch(key string) {
	if u.versions[key] == nil {
		u.versions[key] = map[string]bool{}
	}
}
func (u *universe) version(key, ent string) { u.touch(key); u.versions[key][ent] = true }

func (u *universe) keys(table string) []string {
	var ks []string
	for k := range u.versions {
		if strings.HasPrefix(k, table+"/") {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

func (u *universe) anyVersion(key string, f func(ent string) bool) bool {
	for e := range u.versions[key] {
		if f(e) {
			return true
		}
	}
	return false
}

func strip(c *check.Call) *check.Call {
	cc := *c
	cc.Steps = make([]check.Step, len(c.Steps))
	for i, s := range c.Steps {
		s.Body = nil
		cc.Steps[i] = s
	}
	return &cc
}

// project turns calls into pieces. Statuses: a 2xx is a known outcome; a 5xx or a lost response an
// unknown one; any other status is a violation.
func (m *model) project(phases iter.Seq[[]check.Call], out *check.Projection[In, Out]) error {
	u := &universe{versions: map[string]map[string]bool{}, creators: map[string]bool{}}
	for k, e := range m.seed.initial {
		u.version(k, e)
		u.creators[k] = true
	}
	for k, e := range m.seed.reinit { // a restart can bring these back
		u.version(k, e)
		u.creators[k] = true
	}
	var later []deferred
	for calls := range phases {
		for i := range calls {
			c := &calls[i]
			var info ttapi.Info
			if err := json.Unmarshal(c.Info, &info); err != nil {
				return fmt.Errorf("%s %d: info: %w", c.Op, c.Index, err)
			}
			if c.Op != info.OpName() {
				return fmt.Errorf("%s %d: operation name does not match its info (%s)", c.Op, c.Index, info.OpName())
			}
			ent := ""
			if len(info.Entity) > 0 {
				_, c2, err := ttapi.Canon(info.Table, info.Entity)
				if err != nil {
					return fmt.Errorf("%s %d: entity: %w", c.Op, c.Index, err)
				}
				ent = c2
			}
			write := info.Op == "create" || info.Op == "update" || info.Op == "upsert" || info.Op == "delete"
			unknown := false
			var o ttapi.Out
			if c.Lost() {
				unknown = true
			} else {
				st := c.Steps[len(c.Steps)-1].Status
				switch {
				case st >= 500:
					unknown = true
				case st >= 200 && st < 300:
					o = ttapi.Parse(info, st, c.Steps[len(c.Steps)-1].Body)
					if o.Bad != "" {
						out.Violation(c, "%s: %s", c.Op, o.Bad)
						continue
					}
				default:
					out.Violation(c, "%s answered HTTP %d", c.Op, st)
					continue
				}
			}
			if !unknown {
				for _, e := range o.Ents {
					if info.Op != "query" || !ttapi.IsJoin(info.Q) {
						u.version(ttapi.KeyOf(tableOfOut(info), e), e)
					}
				}
			}
			if unknown && !write {
				continue // an unknown read tells nothing
			}
			gen := ent != "" && info.Generated(ent)
			piece := func(key string, in In, out2 Out) check.Piece[In, Out] {
				return check.Piece[In, Out]{Keys: []string{key}, In: in, Out: out2, Unknown: unknown, Write: write, Origin: c}
			}
			switch info.Op {
			case "create", "update", "upsert", "delete":
				if gen {
					if unknown {
						later = append(later, deferred{kind: "ghost", info: info, table: info.Table, ent: ent, call: strip(c)})
						continue
					}
					if len(o.Ents) != 1 {
						out.Violation(c, "%s: generated-id upsert answered no entity", c.Op)
						continue
					}
					key := ttapi.KeyOf(info.Table, o.Ents[0])
					u.creators[key] = true
					out.Add(piece(key, In{Kind: kGenUpsert, Table: info.Table, E: ent}, Out{Status: o.Status, Ent: o.Ents[0]}))
					continue
				}
				key := info.Table + "/" + info.Key
				if ent != "" {
					key = ttapi.KeyOf(info.Table, ent)
					u.version(key, ent)
				}
				u.touch(key)
				u.creators[key] = true
				in := In{Kind: info.Op, Table: info.Table, E: ent}
				var o2 Out
				if !unknown {
					o2.Status = o.Status
					if len(o.Ents) == 1 {
						o2.Ent = o.Ents[0]
					}
				}
				out.Add(piece(key, in, o2))
			case "get":
				key := info.Table + "/" + info.Key
				u.touch(key)
				o2 := Out{Present: o.Status == 1}
				if len(o.Ents) == 1 {
					o2.Ent = o.Ents[0]
				}
				out.Add(piece(key, In{Kind: kGet, Table: info.Table}, o2))
			case "list":
				later = append(later, deferred{kind: "list", info: info, table: info.Table, out: byKey(info.Table, o.Ents), call: strip(c)})
			case "query":
				if err := m.projectQuery(c, info, o, u, &later, out); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s %d: unknown op %q", c.Op, c.Index, info.Op)
			}
		}
	}
	// pieces that need every key the history mentions
	orphans := map[string][]string{}
	for k := range u.versions {
		if !u.creators[k] {
			orphans[tableOf(k)] = append(orphans[tableOf(k)], k)
		}
	}
	for _, ks := range orphans {
		sort.Strings(ks)
	}
	for _, d := range later {
		add := func(key string, in In, o Out, unknown, write bool) {
			out.Add(check.Piece[In, Out]{Keys: []string{key}, In: in, Out: o, Unknown: unknown, Write: write, Origin: d.call})
		}
		switch d.kind {
		case "ghost":
			for _, k := range orphans[d.table] {
				add(k, In{Kind: kGenUpsert, Table: d.table, E: d.ent}, Out{}, true, true)
			}
		case "list":
			for _, k := range u.keys(d.table) {
				e, ok := d.out[k]
				add(k, In{Kind: kMember, Table: d.table}, Out{Present: ok, Ent: e}, false, false)
			}
		case "set", "none":
			t := ttapi.QueryTable(d.info.Q)
			for _, k := range u.keys(t) {
				e, ok := d.out[k]
				if ok || u.anyVersion(k, func(v string) bool { return ttapi.Matches(d.info.Q, d.info.A, d.info.B, v) }) {
					add(k, In{Kind: kMember, Table: t, Q: d.info.Q, A: d.info.A, B: d.info.B}, Out{Present: ok, Ent: e}, false, false)
				}
			}
		}
	}
	return nil
}

func tableOfOut(i ttapi.Info) string {
	if i.Op == "query" {
		return ttapi.QueryTable(i.Q)
	}
	return i.Table
}

func byKey(table string, ents []string) map[string]string {
	m := map[string]string{}
	for _, e := range ents {
		m[ttapi.KeyOf(table, e)] = e
	}
	return m
}

func (m *model) projectQuery(c *check.Call, info ttapi.Info, o ttapi.Out, u *universe, later *[]deferred, out *check.Projection[In, Out]) error {
	unknown := c.Lost() || c.Steps[len(c.Steps)-1].Status >= 500
	if unknown {
		return nil // reads
	}
	t := ttapi.QueryTable(info.Q)
	add := func(key string, in In, o2 Out) {
		out.Add(check.Piece[In, Out]{Keys: []string{key}, In: in, Out: o2, Origin: c})
	}
	switch {
	case info.Q == "stationName":
		key := "station/" + info.Key
		u.touch(key)
		add(key, In{Kind: kName, Table: "station"}, Out{Present: o.Status == 1, Val: o.Val})
	case ttapi.IsSet(info.Q):
		*later = append(*later, deferred{kind: "set", info: info, table: t, out: byKey(t, o.Ents), call: strip(c)})
	case ttapi.IsFind1(info.Q):
		if o.Status == 0 {
			*later = append(*later, deferred{kind: "none", info: info, table: t, out: map[string]string{}, call: strip(c)})
			return nil
		}
		in := In{Kind: kMember, Table: t, Q: info.Q, A: info.A, B: info.B}
		if info.Q == "stationId" {
			key := "station/" + o.Val
			u.touch(key)
			add(key, in, Out{Present: true})
		} else {
			if len(o.Ents) != 1 {
				out.Violation(c, "%s: found without an entity", c.Op)
				return nil
			}
			add(ttapi.KeyOf(t, o.Ents[0]), in, Out{Present: true, Ent: o.Ents[0]})
		}
	case ttapi.IsJoin(info.Q):
		tk := "travel/" + info.Key
		u.touch(tk)
		in := In{Kind: kJoinTrip, Table: "travel", Q: info.Q}
		if o.Status == 0 {
			if info.Q == "tripRoute" { // status 0 means the trip is absent
				add(tk, in, Out{})
			}
			return nil
		}
		if o.Val == "empty" {
			add(tk, in, Out{Present: true, Empty: true})
			return nil
		}
		if len(o.Ents) != 1 {
			out.Violation(c, "%s: answered without an entity", c.Op)
			return nil
		}
		rt := "route"
		if info.Q == "tripTrain" {
			rt = "train"
		}
		id := ttapi.StrField(o.Ents[0], "id")
		add(tk, in, Out{Present: true, Val: id})
		rk := rt + "/" + id
		u.touch(rk)
		add(rk, In{Kind: kJoinRef, Table: rt, Q: info.Q}, Out{Present: true, Ent: o.Ents[0]})
	default:
		return fmt.Errorf("%s %d: unknown query %q", c.Op, c.Index, info.Q)
	}
	return nil
}
