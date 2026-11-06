// Package ttaccuracy is the train-ticket-k8s accuracy check's workload, on the vibesys-evaluator
// Go SDK: the requests whose answers the oracle (internal/ttoracle) judges. cmd/accuracy runs it
// as the generator of the accuracy run's phases (tt-accuracy; bench.py's accuracy check names it).
//
// Every operation is one request of the six services' public API, through the gateway, with the
// admin token; its Info is the ttapi.Info the oracle reads back, and its name Info.OpName(). The
// mix covers what the benchmark sends (lists, reads by key and by secondary key, full updates,
// route upserts) and what it does not (creates, deletes, server-generated ids, the trip joins),
// over three kinds of keys per table:
//
//   - the fixture the seed Job created (internal/ttdata), which the benchmark works on;
//   - the services' startup catalog (internal/ttcatalog), which a restart writes again;
//   - eight ids of the check's own per table, drawn often, so that writes of one key follow
//     each other closely and reads see them.
//
// Requests do not depend on answers (the stream is generated ahead), so writes reference ids
// from these pools, and a route or price whose id the server generates is never written again.
package ttaccuracy

import (
	"encoding/json"
	"fmt"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttapi"
	"vibesys/train-ticket-k8s-evaluator/internal/ttcatalog"
	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// Extra is how many ids of its own the check draws per table.
const Extra = 8

// pool is the ids of one table the workload draws from.
type pool struct{ fixture, catalog, extra []string }

// draw: 30% one of the check's own ids, 10% a catalog entity, 60% a fixture entity.
func (p pool) draw(r *workload.Rand) string {
	switch x := r.Below(10); {
	case x < 3:
		return p.extra[r.Below(len(p.extra))]
	case x < 4:
		return p.catalog[r.Below(len(p.catalog))]
	}
	return p.fixture[r.Below(len(p.fixture))]
}

// Model is what the requests are drawn from: the admin identity, the pools of ids, and the
// originals of the seeded entities (for secondary-key reads that find something).
type Model struct {
	auth   string
	pools  map[string]pool
	names  []string    // station names to look up: seeded ones and the check's own
	prices [][2]string // the (route, train) of every seeded price
	routes [][]string  // the stations of every seeded route
}

// NewModel is the model of a sample: rand is the sample's model stream, which names the sample's
// admin identity (as the benchmark's does).
func NewModel(data *ttdata.Dataset, rand *workload.Rand) (*Model, error) {
	token := rand.Child("token")
	m := &Model{
		auth:  "Bearer " + ttdata.AdminToken(token.Uint64(), token.Uint64()),
		pools: map[string]pool{},
	}
	fixture := map[string][]string{}
	for _, r := range data.Records {
		fixture[ttdata.Config] = append(fixture[ttdata.Config], r.Config.Name)
		fixture[ttdata.Station] = append(fixture[ttdata.Station], r.StationA.ID, r.StationB.ID)
		fixture[ttdata.Train] = append(fixture[ttdata.Train], r.Train.ID)
		fixture[ttdata.Route] = append(fixture[ttdata.Route], r.Route.ID)
		fixture[ttdata.Price] = append(fixture[ttdata.Price], r.Price.ID)
		fixture[ttdata.Travel] = append(fixture[ttdata.Travel], r.Trip.TripID.String())
		m.names = append(m.names, r.StationA.Name, r.StationB.Name)
		m.prices = append(m.prices, [2]string{r.Price.RouteID, r.Price.TrainType})
		m.routes = append(m.routes, r.Route.Stations)
	}
	for _, table := range ttapi.Tables {
		extra := make([]string, Extra)
		for i := range extra {
			extra[i] = ExtraID(table, i)
		}
		m.pools[table] = pool{fixture: fixture[table], catalog: ttcatalog.IDs(table), extra: extra}
	}
	for _, raw := range ttcatalog.Entities()[ttdata.Station] {
		m.names = append(m.names, field(raw, "name"))
	}
	for _, raw := range ttcatalog.Entities()[ttdata.Price] {
		m.prices = append(m.prices, [2]string{field(raw, "routeId"), field(raw, "trainType")})
	}
	for _, raw := range ttcatalog.Entities()[ttdata.Route] {
		var r struct {
			Stations []string `json:"stations"`
		}
		_ = json.Unmarshal(raw, &r)
		m.routes = append(m.routes, r.Stations)
	}
	for i := range Extra {
		m.names = append(m.names, name(i))
	}
	return m, nil
}

func field(raw json.RawMessage, name string) string {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	s, _ := m[name].(string)
	return s
}

// ExtraID is the check's own id i of table: long enough that the route and price services keep
// it (a shorter one makes them generate one), a UUID for a price (its id's type), and a trip id
// the travel service parses (a type letter and a number).
func ExtraID(table string, i int) string {
	switch table {
	case ttdata.Price:
		return fmt.Sprintf("acc00000-0000-4000-8000-%012x", i)
	case ttdata.Travel:
		return fmt.Sprintf("%s99000%02d", []string{"G", "D"}[i%2], i)
	}
	return fmt.Sprintf("acc-%s-%04d", table, i)
}

// name is the check's own station name i (station names are an index the service answers).
func name(i int) string { return fmt.Sprintf("Acc City %d", i) }

// ---------------------------------------------------------------- operations

type op struct {
	table, kind, q string
	weight         float64
	build          func(m *Model, r *workload.Rand) ttapi.Info
}

// operations is the mix: per table, writes (creates, updates, deletes, upserts) and reads
// (lists, reads by key, secondary-key queries, the trip joins). About 46% of the operations are
// writes and 12% lists.
func operations() []op {
	return []op{
		{"config", "create", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return write("config", "create", m.config(r)) }},
		{"config", "update", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return write("config", "update", m.config(r)) }},
		{"config", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			return byKey("config", "delete", m.pools["config"].draw(r))
		}},
		{"config", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("config") }},
		{"config", "get", "", 3, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("config", "get", m.pools["config"].draw(r)) }},

		{"station", "create", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return write("station", "create", m.station(r)) }},
		{"station", "update", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return write("station", "update", m.station(r)) }},
		{"station", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			// DELETE /stations takes the station; the service deletes by its id.
			e := m.station(r)
			i := write("station", "delete", e)
			i.Key = ttapi.StrField(e, "id")
			return i
		}},
		{"station", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("station") }},
		{"station", "query", "stationName", 2, func(m *Model, r *workload.Rand) ttapi.Info {
			return ttapi.Info{Table: "station", Op: "query", Q: "stationName", Key: m.pools["station"].draw(r)}
		}},
		{"station", "query", "stationId", 2, func(m *Model, r *workload.Rand) ttapi.Info {
			return ttapi.Info{Table: "station", Op: "query", Q: "stationId", A: m.names[r.Below(len(m.names))]}
		}},

		{"train", "create", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return write("train", "create", m.train(r)) }},
		{"train", "update", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return write("train", "update", m.train(r)) }},
		{"train", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("train", "delete", m.pools["train"].draw(r)) }},
		{"train", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("train") }},
		{"train", "get", "", 3, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("train", "get", m.pools["train"].draw(r)) }},

		{"route", "upsert", "", 3, func(m *Model, r *workload.Rand) ttapi.Info {
			id := m.pools["route"].draw(r)
			if r.Below(12) == 0 {
				id = "acc" // shorter than 10: the route service generates the id
			}
			return write("route", "upsert", m.route(r, id))
		}},
		{"route", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("route", "delete", m.pools["route"].draw(r)) }},
		{"route", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("route") }},
		{"route", "get", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("route", "get", m.pools["route"].draw(r)) }},
		{"route", "query", "routeBetween", 2, func(m *Model, r *workload.Rand) ttapi.Info {
			var a, b string
			if r.Below(4) == 0 {
				a, b = m.pools["station"].draw(r), m.pools["station"].draw(r)
			} else { // a seeded route's ends, either way round
				stations := m.routes[r.Below(len(m.routes))]
				a, b = stations[0], stations[len(stations)-1]
				if r.Below(2) == 0 {
					a, b = b, a
				}
			}
			return ttapi.Info{Table: "route", Op: "query", Q: "routeBetween", A: a, B: b}
		}},

		{"price", "upsert", "", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			id := m.pools["price"].draw(r)
			if r.Below(6) == 0 {
				id = "p" // no id: the price service generates one
			}
			return write("price", "upsert", m.price(r, id))
		}},
		{"price", "update", "", 2, func(m *Model, r *workload.Rand) ttapi.Info {
			return write("price", "update", m.price(r, m.pools["price"].draw(r)))
		}},
		{"price", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			// DELETE /prices takes the price; the service deletes by its id.
			e := m.price(r, m.pools["price"].draw(r))
			i := write("price", "delete", e)
			i.Key = ttapi.StrField(e, "id")
			return i
		}},
		{"price", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("price") }},
		{"price", "query", "priceBy", 3, func(m *Model, r *workload.Rand) ttapi.Info {
			rt, tr := m.pools["route"].draw(r), m.pools["train"].draw(r)
			if r.Below(4) != 0 { // a seeded price's route and train
				p := m.prices[r.Below(len(m.prices))]
				rt, tr = p[0], p[1]
			}
			return ttapi.Info{Table: "price", Op: "query", Q: "priceBy", A: rt, B: tr}
		}},

		{"travel", "create", "", 1, func(m *Model, r *workload.Rand) ttapi.Info { return write("travel", "create", m.trip(r)) }},
		{"travel", "update", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return write("travel", "update", m.trip(r)) }},
		{"travel", "delete", "", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			return byKey("travel", "delete", m.pools["travel"].draw(r))
		}},
		{"travel", "list", "", 1, func(*Model, *workload.Rand) ttapi.Info { return list("travel") }},
		{"travel", "get", "", 2, func(m *Model, r *workload.Rand) ttapi.Info { return byKey("travel", "get", m.pools["travel"].draw(r)) }},
		{"travel", "query", "tripsByRoute", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			return ttapi.Info{Table: "travel", Op: "query", Q: "tripsByRoute", A: m.pools["route"].draw(r)}
		}},
		{"travel", "query", "tripRoute", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			return ttapi.Info{Table: "travel", Op: "query", Q: "tripRoute", Key: m.pools["travel"].draw(r)}
		}},
		{"travel", "query", "tripTrain", 1, func(m *Model, r *workload.Rand) ttapi.Info {
			return ttapi.Info{Table: "travel", Op: "query", Q: "tripTrain", Key: m.pools["travel"].draw(r)}
		}},
	}
}

func write(table, kind, entity string) ttapi.Info {
	return ttapi.Info{Table: table, Op: kind, Entity: json.RawMessage(entity)}
}

func byKey(table, kind, key string) ttapi.Info { return ttapi.Info{Table: table, Op: kind, Key: key} }

func list(table string) ttapi.Info { return ttapi.Info{Table: table, Op: "list"} }

// ---------------------------------------------------------------- entities

func canonical(table string, value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	_, canon, err := ttapi.Canon(table, raw)
	if err != nil {
		panic(fmt.Sprintf("ttaccuracy: %s entity %s: %v", table, raw, err))
	}
	return canon
}

func (m *Model) config(r *workload.Rand) string {
	return canonical("config", ttdata.ConfigEntity{
		Name:        m.pools["config"].draw(r),
		Value:       fmt.Sprintf("acc-v-%08x", uint32(r.Uint64())),
		Description: fmt.Sprintf("acc-d-%08x", uint32(r.Uint64())),
	})
}

func (m *Model) station(r *workload.Rand) string {
	return canonical("station", ttdata.StationEntity{
		ID: m.pools["station"].draw(r), Name: m.names[r.Below(len(m.names))], StayTime: 1 + r.Below(60),
	})
}

func (m *Model) train(r *workload.Rand) string {
	return canonical("train", ttdata.TrainEntity{
		ID: m.pools["train"].draw(r), EconomyClass: 50 + r.Below(900), ConfortClass: 20 + r.Below(300), AverageSpeed: 60 + r.Below(300),
	})
}

// route is a route of two or three distinct stations with growing distances.
func (m *Model) route(r *workload.Rand, id string) string {
	n := 2 + r.Below(2)
	stations := make([]string, 0, n)
	for len(stations) < n {
		s := m.pools["station"].draw(r)
		dup := false
		for _, t := range stations {
			dup = dup || t == s
		}
		if !dup {
			stations = append(stations, s)
		}
	}
	distances := []int{0}
	for i := 1; i < n; i++ {
		distances = append(distances, distances[i-1]+10+r.Below(600))
	}
	return canonical("route", ttdata.RouteEntity{
		ID: id, Stations: stations, Distances: distances, StartStationID: stations[0], TerminalStationID: stations[n-1],
	})
}

// price is a price of a pooled route and train type, with rates of two decimals.
func (m *Model) price(r *workload.Rand, id string) string {
	return canonical("price", ttdata.PriceEntity{
		ID: id, RouteID: m.pools["route"].draw(r), TrainType: m.pools["train"].draw(r),
		BasicPriceRate: float64(10+r.Below(90)) / 100, FirstClassPriceRate: float64(100+r.Below(100)) / 100,
	})
}

func (m *Model) trip(r *workload.Rand) string {
	id := m.pools["travel"].draw(r)
	start := int64(1_600_000_000_000) + int64(r.Below(1_000_000))*60_000
	return canonical("travel", ttdata.TripEntity{
		TripID:      ttdata.TripID{Type: id[:1], Number: id[1:]},
		TrainTypeID: m.pools["train"].draw(r), RouteID: m.pools["route"].draw(r),
		StartingTime: start, StartingStationID: m.pools["station"].draw(r),
		StationsID: m.pools["station"].draw(r), TerminalStationID: m.pools["station"].draw(r),
		EndTime: start + int64(1+r.Below(1_200))*60_000,
	})
}

// ---------------------------------------------------------------- the workload

// Request is the one-step request of info, with the admin token; the step expects the status the
// oracle expects of a success (ttapi.ExpectHTTP), so that loadgen counts the rest as errors.
func (m *Model) Request(info ttapi.Info) workload.Request {
	method, path, body, err := ttapi.Request(info)
	if err != nil {
		panic(fmt.Sprintf("ttaccuracy: %v", err))
	}
	options := []workload.Option{workload.Header("Authorization", m.auth), workload.Expect(ttapi.ExpectHTTP(info))}
	if body != nil {
		options = append(options, workload.JSON(body))
	}
	return workload.Request{Steps: []workload.Step{workload.HTTP(method, path, options...)}, Info: info}
}

// Workload is the accuracy check's workload.
func Workload() workload.Workload[*ttdata.Dataset, *Model] {
	var ops []workload.Operation[*Model]
	for _, o := range operations() {
		ops = append(ops, workload.Operation[*Model]{
			Name: ttapi.Info{Table: o.table, Op: o.kind, Q: o.q}.OpName(), Weight: o.weight,
			Build: func(m *Model, r *workload.Rand) workload.Request { return m.Request(o.build(m, r)) },
		})
	}
	return workload.Workload[*ttdata.Dataset, *Model]{Dataset: ttdata.Load, Model: NewModel, Operations: ops}
}
