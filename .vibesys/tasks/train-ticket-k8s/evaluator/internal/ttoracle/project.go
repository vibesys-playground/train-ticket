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

// universe is what the projection knows about the whole history: every key it mentions with
// every version of the key's entity the seed or the history shows, and the keys some write (or
// the seed) brings into being. A listing, an index query that found nothing and a lost
// generated-id upsert need it, so Project collects it in a first pass and adds the pieces in a
// second.
type universe struct {
	versions map[string]map[string]bool // key -> every canonical version the seed or the history mentions
	creators map[string]bool            // keys some write or the seed brings into being
	orphans  map[string][]string        // table -> keys no write or seed brings into being, sorted
}

func newUniverse(sd *seed) *universe {
	u := &universe{versions: map[string]map[string]bool{}, creators: map[string]bool{}}
	for k, e := range sd.initial {
		u.version(k, e)
		u.creators[k] = true
	}
	for k, e := range sd.reinit { // a restart can bring these back
		u.version(k, e)
		u.creators[k] = true
	}
	return u
}

func (u *universe) touch(key string) {
	if u.versions[key] == nil {
		u.versions[key] = map[string]bool{}
	}
}
func (u *universe) version(key, ent string) { u.touch(key); u.versions[key][ent] = true }

// seal computes the orphans once every key is known.
func (u *universe) seal() {
	u.orphans = map[string][]string{}
	for k := range u.versions {
		if !u.creators[k] {
			u.orphans[tableOf(k)] = append(u.orphans[tableOf(k)], k)
		}
	}
	for _, ks := range u.orphans {
		sort.Strings(ks)
	}
}

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

// parsed is a call as the projection reads it.
type parsed struct {
	info    ttapi.Info
	ent     string // the canonical entity the request carries, or ""
	write   bool
	unknown bool      // a lost response or a 5xx: the outcome is unknown
	out     ttapi.Out // the parsed response of a known outcome
	bad     string    // a violation: the response is wrong on its own
}

// parse reads a call: statuses: a 2xx is a known outcome; a 5xx or a lost response an unknown
// one; any other status, a malformed body or a schema violation is a violation. An error is a
// task bug (an info the oracle does not know).
func parse(c *check.Call) (parsed, error) {
	var p parsed
	if err := json.Unmarshal(c.Info, &p.info); err != nil {
		return p, fmt.Errorf("%s %d: info: %w", c.Op, c.Index, err)
	}
	if c.Op != p.info.OpName() {
		return p, fmt.Errorf("%s %d: operation name does not match its info (%s)", c.Op, c.Index, p.info.OpName())
	}
	if len(c.Steps) != 1 {
		return p, fmt.Errorf("%s %d: %d steps, the accuracy workload sends one", c.Op, c.Index, len(c.Steps))
	}
	if len(p.info.Entity) > 0 {
		_, canon, err := ttapi.Canon(p.info.Table, p.info.Entity)
		if err != nil {
			return p, fmt.Errorf("%s %d: entity: %w", c.Op, c.Index, err)
		}
		p.ent = canon
	}
	switch p.info.Op {
	case "create", "update", "upsert", "delete":
		p.write = true
	case "get", "list", "query":
	default:
		return p, fmt.Errorf("%s %d: unknown op %q", c.Op, c.Index, p.info.Op)
	}
	if p.info.Op == "query" {
		switch {
		case p.info.Q == "stationName", ttapi.IsFind1(p.info.Q), ttapi.IsSet(p.info.Q), ttapi.IsJoin(p.info.Q):
		default:
			return p, fmt.Errorf("%s %d: unknown query %q", c.Op, c.Index, p.info.Q)
		}
	}
	if c.Lost() {
		p.unknown = true
		return p, nil
	}
	st := c.Steps[0].Status
	switch {
	case st >= 500:
		p.unknown = true
	case st >= 200 && st < 300:
		p.out = ttapi.Parse(p.info, st, c.Steps[0].Body)
		if p.out.Bad != "" {
			p.bad = p.out.Bad
		}
	default:
		p.bad = fmt.Sprintf("answered HTTP %d", st)
	}
	return p, nil
}

// generated says whether the server picks the write's id (a route or price upsert with a short id).
func (p *parsed) generated() bool { return p.ent != "" && p.info.Generated(p.ent) }

// learn adds what a call shows about the whole history to the universe (the first pass).
func (u *universe) learn(p *parsed) {
	if p.bad != "" {
		return
	}
	info, o := p.info, p.out
	if !p.unknown {
		for _, e := range o.Ents {
			if info.Op != "query" || !ttapi.IsJoin(info.Q) {
				u.version(ttapi.KeyOf(tableOfOut(info), e), e)
			}
		}
	}
	if p.unknown && !p.write {
		return
	}
	switch info.Op {
	case "create", "update", "upsert", "delete":
		if p.generated() {
			if !p.unknown && len(o.Ents) == 1 {
				u.creators[ttapi.KeyOf(info.Table, o.Ents[0])] = true
			}
			return
		}
		key := info.Table + "/" + info.Key
		if p.ent != "" {
			key = ttapi.KeyOf(info.Table, p.ent)
			u.version(key, p.ent)
		}
		u.touch(key)
		u.creators[key] = true
	case "get":
		u.touch(info.Table + "/" + info.Key)
	case "query":
		switch {
		case info.Q == "stationName":
			u.touch("station/" + info.Key)
		case info.Q == "stationId" && o.Status == 1:
			u.touch("station/" + o.Val)
		case ttapi.IsJoin(info.Q):
			u.touch("travel/" + info.Key)
			if o.Status == 1 && o.Val != "empty" && len(o.Ents) == 1 {
				rt := "route"
				if info.Q == "tripTrain" {
					rt = "train"
				}
				u.touch(rt + "/" + ttapi.StrField(o.Ents[0], "id"))
			}
		}
	}
}

// project turns calls into pieces, in two passes over the history: the universe (every key and
// version, the keys writes bring into being), then the pieces. A call whose outcome touches no
// key it can be checked against (an unknown read, a lost generated-id upsert with no key it could
// have made, a query nothing could have matched) becomes a piece without keys: it is counted (an
// unknown one toward the lost share) and not searched.
func (m *model) project(phases iter.Seq[[]check.Call], out *check.Projection[In, Out]) error {
	u := newUniverse(m.seed)
	for calls := range phases {
		for i := range calls {
			p, err := parse(&calls[i])
			if err != nil {
				return err
			}
			u.learn(&p)
		}
	}
	u.seal()
	for calls := range phases {
		for i := range calls {
			c := &calls[i]
			p, err := parse(c)
			if err != nil {
				return err
			}
			if p.bad != "" {
				out.Violation(c, "%s: %s", c.Op, p.bad)
				continue
			}
			if n := m.pieces(c, &p, u, out); n == 0 {
				out.Add(check.Piece[In, Out]{In: In{Kind: p.info.Op, Table: p.info.Table, Q: p.info.Q}, Unknown: p.unknown, Write: p.write, Origin: c})
			}
		}
	}
	return nil
}

// pieces adds the pieces of one call and returns how many it added.
func (m *model) pieces(c *check.Call, p *parsed, u *universe, out *check.Projection[In, Out]) int {
	info, o := p.info, p.out
	n := 0
	add := func(key string, in In, o2 Out) {
		out.Add(check.Piece[In, Out]{Keys: []string{key}, In: in, Out: o2, Unknown: p.unknown, Write: p.write, Origin: c})
		n++
	}
	if p.unknown && !p.write {
		return 0 // an unknown read tells nothing
	}
	switch info.Op {
	case "create", "update", "upsert", "delete":
		if p.generated() {
			if p.unknown { // it may have made any key no acknowledged write claims
				for _, k := range u.orphans[info.Table] {
					add(k, In{Kind: kGenUpsert, Table: info.Table, E: p.ent}, Out{})
				}
				return n
			}
			if len(o.Ents) != 1 {
				out.Violation(c, "%s: generated-id upsert answered no entity", c.Op)
				return -1
			}
			add(ttapi.KeyOf(info.Table, o.Ents[0]), In{Kind: kGenUpsert, Table: info.Table, E: p.ent}, Out{Status: o.Status, Ent: o.Ents[0]})
			return n
		}
		key := info.Table + "/" + info.Key
		if p.ent != "" {
			key = ttapi.KeyOf(info.Table, p.ent)
		}
		var o2 Out
		if !p.unknown {
			o2.Status = o.Status
			if len(o.Ents) == 1 {
				o2.Ent = o.Ents[0]
			}
		}
		add(key, In{Kind: info.Op, Table: info.Table, E: p.ent}, o2)
	case "get":
		o2 := Out{Present: o.Status == 1}
		if len(o.Ents) == 1 {
			o2.Ent = o.Ents[0]
		}
		add(info.Table+"/"+info.Key, In{Kind: kGet, Table: info.Table}, o2)
	case "list":
		got := byKey(info.Table, o.Ents)
		for _, k := range u.keys(info.Table) {
			e, ok := got[k]
			add(k, In{Kind: kMember, Table: info.Table}, Out{Present: ok, Ent: e})
		}
	case "query":
		return m.queryPieces(c, info, o, u, add, out)
	}
	return n
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

// queryPieces adds the pieces of a query with a known outcome and returns how many it added (-1
// after a violation).
func (m *model) queryPieces(c *check.Call, info ttapi.Info, o ttapi.Out, u *universe, add func(string, In, Out), out *check.Projection[In, Out]) int {
	n := 0
	add1 := func(key string, in In, o2 Out) { add(key, in, o2); n++ }
	t := ttapi.QueryTable(info.Q)
	// matching: every key some version of which the query could have returned, and the keys
	// the answer holds.
	matching := func(got map[string]string) {
		for _, k := range u.keys(t) {
			e, ok := got[k]
			if ok || u.anyVersion(k, func(v string) bool { return ttapi.Matches(info.Q, info.A, info.B, v) }) {
				add1(k, In{Kind: kMember, Table: t, Q: info.Q, A: info.A, B: info.B}, Out{Present: ok, Ent: e})
			}
		}
	}
	switch {
	case info.Q == "stationName":
		add1("station/"+info.Key, In{Kind: kName, Table: "station"}, Out{Present: o.Status == 1, Val: o.Val})
	case ttapi.IsSet(info.Q):
		matching(byKey(t, o.Ents))
	case ttapi.IsFind1(info.Q):
		if o.Status == 0 {
			matching(map[string]string{})
			return n
		}
		in := In{Kind: kMember, Table: t, Q: info.Q, A: info.A, B: info.B}
		if info.Q == "stationId" {
			add1("station/"+o.Val, in, Out{Present: true})
			return n
		}
		if len(o.Ents) != 1 {
			out.Violation(c, "%s: found without an entity", c.Op)
			return -1
		}
		add1(ttapi.KeyOf(t, o.Ents[0]), in, Out{Present: true, Ent: o.Ents[0]})
	case ttapi.IsJoin(info.Q):
		tk := "travel/" + info.Key
		in := In{Kind: kJoinTrip, Table: "travel", Q: info.Q}
		if o.Status == 0 {
			if info.Q == "tripRoute" { // status 0 means the trip is absent
				add1(tk, in, Out{})
			}
			// tripTrain answers 0 for an absent trip or an absent train: nothing to check
			return n
		}
		if o.Val == "empty" {
			add1(tk, in, Out{Present: true, Empty: true})
			return n
		}
		if len(o.Ents) != 1 {
			out.Violation(c, "%s: answered without an entity", c.Op)
			return -1
		}
		rt := "route"
		if info.Q == "tripTrain" {
			rt = "train"
		}
		id := ttapi.StrField(o.Ents[0], "id")
		add1(tk, in, Out{Present: true, Val: id})
		add1(rt+"/"+id, In{Kind: kJoinRef, Table: rt, Q: info.Q}, Out{Present: true, Ent: o.Ents[0]})
	}
	return n
}
