// Package ttfake: in-memory concurrent fake of the Train Ticket APIs the oracle covers (tests only),
// with injectable bugs. Reads of collections are NOT snapshots (like Mongo findAll): each
// entity is read at its own instant. Joins read the trip, then the referenced entity.
package ttfake

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"
)

// In is the fake's view of an operation: Info with the entity canonicalised.
type In struct{ Op, T, K, E, Q, A, B string }

// Do executes one operation and returns (HTTP status, body).
func (f *Fake) DoInfo(i ttapi.Info) (int, []byte) {
	in := In{Op: i.Op, T: i.Table, K: i.Key, Q: i.Q, A: i.A, B: i.B}
	if len(i.Entity) > 0 {
		_, c, err := ttapi.Canon(i.Table, i.Entity)
		if err != nil {
			panic(err)
		}
		in.E = c
	}
	return f.Do(in)
}

type Bugs struct {
	LostUpdate    float64 // probability an update/upsert of an existing entity is acknowledged but dropped
	StaleIndex    bool    // station-name and price (route,train) lookups use append-only caches
	DanglingRef   bool    // trip->route join reads a route cache that is not invalidated by delete
	Resurrect     bool    // delete removes only the cache; a restart reloads the entity from durable storage
	BadSchema     float64 // probability a returned entity has a wrong field type / missing field
	IDCollision   bool    // every 5th server-generated id re-issues an old one
	ListDropsLast bool    // list omits its last element
}

type Fake struct {
	mu         sync.Mutex
	durable    map[string]map[string]string // table -> key -> canonical entity
	cache      map[string]map[string]string
	idxSt      map[string]string // station name -> id (append-only)
	idxPr      map[string]string // route|train -> entity snapshot (append-only)
	refRt      map[string]string // route id -> entity (not invalidated on delete when DanglingRef)
	gen        int
	issued     []string
	Bugs       Bugs
	Delay      time.Duration
	Reinit     bool
	ReinitSeed map[string]string // what a restart re-applies (InitData's catalog); the seed when nil
	EmptyRoute bool
	seed       map[string]string
	nrestart   int
}

func New(seed map[string]string, b Bugs) *Fake {
	f := &Fake{durable: map[string]map[string]string{}, cache: map[string]map[string]string{}, idxSt: map[string]string{},
		idxPr: map[string]string{}, refRt: map[string]string{}, Bugs: b, Delay: 40 * time.Microsecond, seed: seed}
	for _, t := range ttapi.Tables {
		f.durable[t] = map[string]string{}
	}
	for k, e := range seed {
		t, id, _ := strings.Cut(k, "/")
		f.durable[t][id] = e
	}
	f.restart()
	for t, m := range f.cache {
		for id, e := range m {
			f.index(t, id, e)
		}
	}
	return f
}

// Restart: a rolling restart reloads caches from durable storage.
// Restart restarts the service of one table (round robin). With Reinit the service's startup
// code re-applies the seed like Train Ticket's InitData: create-if-absent, upsert for route/price.
func (f *Fake) Restart() (kind, target string) {
	if !f.Reinit {
		f.restart()
		return "restart", ""
	}
	t := ttapi.Tables[f.nrestart%len(ttapi.Tables)]
	f.nrestart++
	return f.RestartService(t)
}

// RestartService restarts the service of table t: caches reload from durable storage and, with
// Reinit, its startup code re-applies its part of the seed (ReinitSeed when set).
func (f *Fake) RestartService(t string) (kind, target string) {
	f.restart()
	if f.Reinit {
		seed := f.seed
		if f.ReinitSeed != nil {
			seed = f.ReinitSeed
		}
		f.mu.Lock()
		for k, e := range seed {
			tt, id, _ := strings.Cut(k, "/")
			if tt != t {
				continue
			}
			if _, ex := f.cache[t][id]; !ex || t == "route" || t == "price" {
				f.put(t, id, e)
			}
		}
		f.mu.Unlock()
	}
	return "restart", "ts-" + t + "-service" // the service's Deployment, as the accuracy run names it
}

func (f *Fake) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache = map[string]map[string]string{}
	for t, m := range f.durable {
		f.cache[t] = map[string]string{}
		for k, v := range m {
			f.cache[t][k] = v
		}
	}
}

func (f *Fake) jitter() {
	if f.Delay > 0 {
		time.Sleep(time.Duration(rand.Int63n(int64(f.Delay))))
	}
}

func (f *Fake) index(t, id, e string) {
	switch t {
	case "station":
		f.idxSt[field(e, "name")] = id
	case "price":
		f.idxPr[field(e, "routeId")+"|"+field(e, "trainType")] = e
	case "route":
		f.refRt[id] = e
	}
}

func field(e, f string) string { s, _ := ttmodelFields(e)[f].(string); return s }
func ttmodelFields(e string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(e), &m)
	return m
}

func resp(status int, data string) []byte {
	if data == "" {
		data = "null"
	}
	return []byte(fmt.Sprintf(`{"status":%d,"msg":"m","data":%s}`, status, data))
}

func (f *Fake) corrupt(t, e string) string {
	if f.Bugs.BadSchema == 0 || rand.Float64() >= f.Bugs.BadSchema {
		return e
	}
	m := ttmodelFields(e)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	k := keys[rand.Intn(len(keys))]
	switch rand.Intn(3) {
	case 0:
		delete(m, k)
	case 1:
		m[k] = fmt.Sprint(m[k]) // number/list -> string
	default:
		m["extra"] = 1
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (f *Fake) newID() string {
	f.gen++
	if f.Bugs.IDCollision && f.gen%5 == 0 && len(f.issued) > 0 {
		return f.issued[rand.Intn(len(f.issued))]
	}
	id := fmt.Sprintf("00000000-0000-4000-9000-%012d", f.gen)
	f.issued = append(f.issued, id)
	return id
}

func (f *Fake) put(t, id, e string) {
	f.durable[t][id] = e
	f.cache[t][id] = e
	f.index(t, id, e)
}

func (f *Fake) del(t, id string) {
	delete(f.cache[t], id)
	if !f.Bugs.Resurrect {
		delete(f.durable[t], id)
	}
	if t == "route" && !f.Bugs.DanglingRef {
		delete(f.refRt, id)
	}
}

func keyOf(t, e string) string { return strings.TrimPrefix(ttapi.KeyOf(t, e), t+"/") }

func (f *Fake) lostUpdate() bool {
	return f.Bugs.LostUpdate > 0 && rand.Float64() < f.Bugs.LostUpdate
}

// Do executes one call and returns (HTTP status, body).
func (f *Fake) Do(in In) (int, []byte) {
	f.jitter()
	switch in.Op {
	case "list":
		return f.list(in.T, func(string) bool { return true })
	case "query":
		return f.query(in)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t := in.T
	tcDup, tcUpd, tcDel := 0, 0, 0
	switch t {
	case "travel":
		tcDup, tcUpd = 1, 1
	case "route":
		tcDel = 1
	}
	ok201 := 200
	if in.Op == "create" && t != "train" && t != "route" || in.Op == "upsert" && t == "price" {
		ok201 = 201
	}
	switch in.Op {
	case "create":
		id := keyOf(t, in.E)
		if _, ex := f.cache[t][id]; ex {
			return ok201, resp(tcDup, "")
		}
		f.put(t, id, in.E)
		return ok201, resp(1, "")
	case "update":
		id := keyOf(t, in.E)
		if _, ex := f.cache[t][id]; !ex {
			return ok201, resp(tcUpd, "")
		}
		if !f.lostUpdate() {
			f.put(t, id, in.E)
		}
		return ok201, resp(1, "")
	case "upsert":
		e := in.E
		id := field(e, "id")
		if len(id) < 10 {
			id = f.newID()
			e = ttapi.WithID(e, id)
			f.put(t, id, e)
			return ok201, resp(1, e)
		}
		if _, ex := f.cache[t][id]; ex && f.lostUpdate() {
			return ok201, resp(1, e)
		}
		f.put(t, id, e)
		return ok201, resp(1, e)
	case "delete":
		_, ex := f.cache[t][in.K]
		f.del(t, in.K)
		if !ex {
			return ok201, resp(tcDel, "")
		}
		return ok201, resp(1, "")
	case "get":
		e, ex := f.cache[t][in.K]
		if !ex {
			return 200, resp(0, "")
		}
		return 200, resp(1, f.corrupt(t, e))
	}
	panic("bad op")
}

// list: keys are snapshotted, entities are then read one at a time (no snapshot isolation).
func (f *Fake) list(t string, pred func(string) bool) (int, []byte) {
	f.mu.Lock()
	var keys []string
	for k := range f.cache[t] {
		keys = append(keys, k)
	}
	f.mu.Unlock()
	sort.Strings(keys)
	var parts []string
	for i, k := range keys {
		if i%4 == 3 {
			f.jitter()
		}
		f.mu.Lock()
		e, ok := f.cache[t][k]
		f.mu.Unlock()
		if ok && pred(e) {
			parts = append(parts, f.corrupt(t, e))
		}
	}
	if f.Bugs.ListDropsLast && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return 200, resp(0, "")
	}
	return 200, resp(1, "["+strings.Join(parts, ",")+"]")
}

func (f *Fake) query(in In) (int, []byte) {
	switch in.Q {
	case "stationName":
		f.mu.Lock()
		defer f.mu.Unlock()
		e, ok := f.cache["station"][in.K]
		if !ok {
			return 200, resp(0, "")
		}
		b, _ := json.Marshal(field(e, "name"))
		return 200, resp(1, string(b))
	case "stationId":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.Bugs.StaleIndex {
			if id, ok := f.idxSt[in.A]; ok {
				b, _ := json.Marshal(id)
				return 200, resp(1, string(b))
			}
			return 200, resp(0, "")
		}
		var ids []string
		for id, e := range f.cache["station"] {
			if field(e, "name") == in.A {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return 200, resp(0, "")
		}
		sort.Strings(ids)
		b, _ := json.Marshal(ids[rand.Intn(len(ids))])
		return 200, resp(1, string(b))
	case "priceBy":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.Bugs.StaleIndex {
			if e, ok := f.idxPr[in.A+"|"+in.B]; ok {
				return 200, resp(1, e)
			}
			return 200, resp(0, "")
		}
		for _, e := range f.cache["price"] {
			if field(e, "routeId") == in.A && field(e, "trainType") == in.B {
				return 200, resp(1, e)
			}
		}
		return 200, resp(0, "")
	case "routeBetween":
		return f.list("route", func(e string) bool {
			st := ttmodelFields(e)["stations"].([]any)
			a, b := -1, -1
			for i, x := range st {
				if x == in.A && a < 0 {
					a = i
				}
				if x == in.B && b < 0 {
					b = i
				}
			}
			return a >= 0 && b >= 0 && a < b
		})
	case "tripsByRoute":
		_, body := f.list("travel", func(e string) bool { return field(e, "routeId") == in.A })
		var env struct{ Data json.RawMessage }
		_ = json.Unmarshal(body, &env)
		d := string(env.Data)
		if d == "null" || d == "" {
			d = "[]"
		}
		return 200, resp(1, "["+d+"]")
	case "tripRoute", "tripTrain":
		f.mu.Lock()
		trip, ok := f.cache["travel"][in.K]
		f.mu.Unlock()
		if !ok {
			return 200, resp(0, "")
		}
		f.jitter() // the real service makes an HTTP call to the route/train service here
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.Q == "tripRoute" {
			rid := field(trip, "routeId")
			if f.Bugs.DanglingRef {
				if e, ok := f.refRt[rid]; ok {
					return 200, resp(1, e)
				}
			} else if e, ok := f.cache["route"][rid]; ok {
				return 200, resp(1, e)
			}
			if f.EmptyRoute { // like the real travel service
				return 200, resp(1, `{"id":null,"stations":null,"distances":null,"startStationId":null,"terminalStationId":null}`)
			}
			return 200, resp(0, "")
		}
		if e, ok := f.cache["train"][field(trip, "trainTypeId")]; ok {
			return 200, resp(1, e)
		}
		return 200, resp(0, "")
	}
	panic("bad query")
}
