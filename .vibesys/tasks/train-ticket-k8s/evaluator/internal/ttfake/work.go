// Synthetic workload: a concurrent random stream over a small pool of ids (to force contention)
// that records check.Calls, with a restart between phases. Tests only.
package ttfake

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

// Doer is the system under test: a fake here.
type Doer interface {
	Do(in In) (int, []byte) // (HTTP status, body); status<=0 means transport failure
	Restart() (kind, target string)
}

func canon(table string, m map[string]any) string {
	b, _ := json.Marshal(m)
	_, c, err := ttapi.Canon(table, b)
	if err != nil {
		panic(err)
	}
	return c
}

func Station(id, name string, stay int) string {
	return canon("station", map[string]any{"id": id, "name": name, "stayTime": stay})
}
func Train(id string, e, c, sp int) string {
	return canon("train", map[string]any{"id": id, "economyClass": e, "confortClass": c, "averageSpeed": sp})
}
func Route(id string, st []string, d []int) string {
	return canon("route", map[string]any{"id": id, "stations": st, "distances": d, "startStationId": st[0], "terminalStationId": st[len(st)-1]})
}
func Price(id, route, train string, b, f float64) string {
	return canon("price", map[string]any{"id": id, "trainType": train, "routeId": route, "basicPriceRate": b, "firstClassPriceRate": f})
}
func Trip(ty, num, train, route, from, to string, t0, t1 int) string {
	return canon("travel", map[string]any{"tripId": map[string]any{"type": ty, "number": num}, "trainTypeId": train, "routeId": route,
		"startingTime": t0, "startingStationId": from, "stationsId": to, "terminalStationId": to, "endTime": t1})
}
func Config(name, v, d string) string {
	return canon("config", map[string]any{"name": name, "value": v, "description": d})
}

// Seed mimics seed_catalog.json (a few of each entity).
func Seed() map[string]string {
	s := map[string]string{}
	add := func(e string, t string) { s[ttapi.KeyOf(t, e)] = e }
	add(Config("DirectTicketAllocationProportion", "0.5", "seed"), "config")
	for i, n := range []string{"beijing", "shanghai", "nanjing"} {
		add(Station(n, "Name "+n, 3+i), "station")
	}
	add(Train("TeKuai", 2147483647, 2147483647, 120), "train")
	add(Train("ZhiDa", 2147483647, 2147483647, 120), "train")
	add(Route("seed-route-000001", []string{"shanghai", "nanjing", "beijing"}, []int{0, 100, 800}), "route")
	add(Price("seed-price-000001", "seed-route-000001", "TeKuai", 0.5, 0.9), "price")
	add(Trip("G", "1234", "TeKuai", "seed-route-000001", "shanghai", "beijing", 1000, 2000), "travel")
	return s
}

type Params struct {
	Seed            int64
	Clients, Ops    int // per client per phase
	Phases          int
	Stations, Names int
	Trains, Routes  int
	Prices, Trips   int
	Configs         int
	Unknown         float64 // fraction of writes whose response is lost
	SampleReads     float64 // fraction of reads kept in the history (1 = all)
	WriteFrac       float64
}

func Defaults(seed int64) Params {
	return Params{Seed: seed, Clients: 6, Ops: 40, Phases: 3, Stations: 5, Names: 6, Trains: 3, Routes: 3, Prices: 3, Trips: 4, Configs: 3, SampleReads: 1, WriteFrac: 0.5}
}

type run struct {
	p   Params
	mu  sync.Mutex
	gen []string // server-generated route ids seen
	t0  time.Time
}

func (r *run) now() int64 { return int64(time.Since(r.t0)) }

func pick(rng *rand.Rand, prefix string, n int) string {
	return fmt.Sprintf("%s%02d", prefix, rng.Intn(n))
}

func (r *run) routeRef(rng *rand.Rand) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.gen) > 0 && rng.Intn(3) == 0 {
		return r.gen[rng.Intn(len(r.gen))]
	}
	if rng.Intn(5) == 0 {
		return "seed-route-000001"
	}
	return fmt.Sprintf("route-long-%02d", rng.Intn(r.p.Routes))
}

func (r *run) stationID(rng *rand.Rand) string {
	if rng.Intn(8) == 0 {
		return []string{"beijing", "shanghai", "nanjing"}[rng.Intn(3)]
	}
	return pick(rng, "station-", r.p.Stations)
}
func (r *run) name(rng *rand.Rand) string { return fmt.Sprintf("City %d", rng.Intn(r.p.Names)) }
func (r *run) trainID(rng *rand.Rand) string {
	if rng.Intn(6) == 0 {
		return "TeKuai"
	}
	return pick(rng, "train-", r.p.Trains)
}

func (r *run) next(rng *rand.Rand) In {
	w := rng.Float64() < r.p.WriteFrac
	switch rng.Intn(6) {
	case 0: // station
		id := r.stationID(rng)
		if w {
			e := Station(id, r.name(rng), rng.Intn(20))
			switch rng.Intn(4) {
			case 0:
				return In{Op: "create", T: "station", E: e}
			case 1, 2:
				return In{Op: "update", T: "station", E: e}
			}
			return In{Op: "delete", T: "station", E: e, K: id}
		}
		switch rng.Intn(4) {
		case 0:
			return In{Op: "list", T: "station"}
		case 1:
			return In{Op: "query", T: "station", Q: "stationName", K: id}
		}
		return In{Op: "query", T: "station", Q: "stationId", A: r.name(rng)}
	case 1: // train
		id := r.trainID(rng)
		if w {
			e := Train(id, rng.Intn(1000), rng.Intn(1000), 100+rng.Intn(150))
			return []In{{Op: "create", T: "train", E: e}, {Op: "update", T: "train", E: e}, {Op: "update", T: "train", E: e}, {Op: "delete", T: "train", K: id}}[rng.Intn(4)]
		}
		if rng.Intn(4) == 0 {
			return In{Op: "list", T: "train"}
		}
		return In{Op: "get", T: "train", K: id}
	case 2: // route
		if w {
			if rng.Intn(5) == 0 {
				return In{Op: "delete", T: "route", K: r.routeRef(rng)}
			}
			id := ""
			if rng.Intn(3) == 0 {
				id = "r" // short: the server generates the id
			} else {
				id = fmt.Sprintf("route-long-%02d", rng.Intn(r.p.Routes))
			}
			a, b := r.stationID(rng), r.stationID(rng)
			st := []string{a, b}
			d := []int{0, 1 + rng.Intn(500)}
			if rng.Intn(2) == 0 {
				st = []string{a, r.stationID(rng), b}
				d = []int{0, 1 + rng.Intn(100), 200 + rng.Intn(400)}
			}
			return In{Op: "upsert", T: "route", E: Route(id, st, d)}
		}
		switch rng.Intn(3) {
		case 0:
			return In{Op: "list", T: "route"}
		case 1:
			return In{Op: "query", T: "route", Q: "routeBetween", A: r.stationID(rng), B: r.stationID(rng)}
		}
		return In{Op: "get", T: "route", K: r.routeRef(rng)}
	case 3: // price
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", rng.Intn(r.p.Prices))
		rt, tr := r.routeRef(rng), r.trainID(rng)
		if w {
			e := Price(id, rt, tr, float64(rng.Intn(100))/10, float64(rng.Intn(100))/10)
			switch rng.Intn(4) {
			case 0:
				return In{Op: "upsert", T: "price", E: e}
			case 1:
				return In{Op: "upsert", T: "price", E: Price("p", rt, tr, 1.5, 2.5)} // generated id
			case 2:
				return In{Op: "update", T: "price", E: e}
			}
			return In{Op: "delete", T: "price", E: e, K: id}
		}
		if rng.Intn(3) == 0 {
			return In{Op: "list", T: "price"}
		}
		return In{Op: "query", T: "price", Q: "priceBy", A: rt, B: tr}
	case 4: // trip
		num := fmt.Sprint(1 + rng.Intn(r.p.Trips))
		ty := []string{"G", "D"}[rng.Intn(2)]
		if w {
			e := Trip(ty, num, r.trainID(rng), r.routeRef(rng), r.stationID(rng), r.stationID(rng), rng.Intn(1000), 1000+rng.Intn(1000))
			return []In{{Op: "create", T: "travel", E: e}, {Op: "update", T: "travel", E: e}, {Op: "update", T: "travel", E: e}, {Op: "delete", T: "travel", K: ty + num}}[rng.Intn(4)]
		}
		k := ty + num
		switch rng.Intn(5) {
		case 0:
			return In{Op: "list", T: "travel"}
		case 1:
			return In{Op: "query", T: "travel", Q: "tripsByRoute", A: r.routeRef(rng)}
		case 2:
			return In{Op: "query", T: "travel", Q: "tripRoute", K: k}
		case 3:
			return In{Op: "query", T: "travel", Q: "tripTrain", K: k}
		}
		return In{Op: "get", T: "travel", K: k}
	}
	name := fmt.Sprintf("Cfg%d", rng.Intn(r.p.Configs))
	if w {
		e := Config(name, fmt.Sprint(rng.Intn(1000)), "d")
		return []In{{Op: "create", T: "config", E: e}, {Op: "update", T: "config", E: e}, {Op: "delete", T: "config", K: name}}[rng.Intn(3)]
	}
	if rng.Intn(4) == 0 {
		return In{Op: "list", T: "config"}
	}
	return In{Op: "get", T: "config", K: name}
}

func info(in In) ttapi.Info {
	i := ttapi.Info{Table: in.T, Op: in.Op, Key: in.K, Q: in.Q, A: in.A, B: in.B}
	if in.E != "" {
		i.Entity = json.RawMessage(in.E)
	}
	return i
}

// Record runs the workload against d: p.Phases phases of p.Clients closed-loop clients, with a
// restart fault between phases (quiescent: every client has finished). Times are nanoseconds since
// the start.
func Record(d Doer, p Params) ([][]check.Call, []check.Fault) {
	r := &run{p: p, t0: time.Now()}
	var phases [][]check.Call
	var faults []check.Fault
	for ph := 0; ph < p.Phases; ph++ {
		var mu sync.Mutex
		var calls []check.Call
		var wg sync.WaitGroup
		for c := 0; c < p.Clients; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(p.Seed*1000003 + int64(ph*100+c)))
				for i := 0; i < p.Ops; i++ {
					in := r.next(rng)
					pure := in.Op == "get" || in.Op == "list" || in.Op == "query"
					if pure && rng.Float64() >= p.SampleReads {
						continue
					}
					lost := !pure && rng.Float64() < p.Unknown
					start := r.now()
					st, body := -1, []byte(nil)
					if !(lost && rng.Intn(2) == 0) { // a lost response may or may not have been applied
						st, body = d.Do(in)
					}
					end := r.now()
					if lost {
						st, body = 0, nil
					} else if st < 0 {
						st = 0
					}
					inf := info(in)
					raw, _ := json.Marshal(inf)
					cl := check.Call{Phase: ph, Client: c, Op: inf.OpName(), Info: raw, Start: start, End: end,
						Steps: []check.Step{{Start: start, End: end, Status: st, Body: body}}}
					if inf.Op == "upsert" && inf.Table == "route" && st == 200 {
						if o := ttapi.Parse(inf, st, body); o.Bad == "" && len(o.Ents) == 1 {
							r.mu.Lock()
							r.gen = append(r.gen, ttapi.KeyOf("route", o.Ents[0])[len("route/"):])
							r.mu.Unlock()
						}
					}
					mu.Lock()
					cl.Index = uint64(len(calls))
					calls = append(calls, cl)
					mu.Unlock()
				}
			}(c)
		}
		wg.Wait()
		sort.Slice(calls, func(i, j int) bool { return calls[i].Start < calls[j].Start })
		for i := range calls {
			calls[i].Index = uint64(i)
		}
		phases = append(phases, calls)
		if ph < p.Phases-1 {
			time.Sleep(time.Millisecond)
			t := r.now()
			kind, target := d.Restart()
			time.Sleep(time.Millisecond)
			faults = append(faults, check.Fault{Kind: kind, Target: target, Start: t, End: r.now(), AfterPhase: ph})
		}
	}
	return phases, faults
}

// JoinRoute is a route as GET /travelservice/routes/{trip} returns it: id, stations and distances,
// with the start and terminal station ids null.
func JoinRoute(route string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(route), &m)
	m["startStationId"], m["terminalStationId"] = nil, nil
	b, _ := json.Marshal(m)
	return string(b)
}
