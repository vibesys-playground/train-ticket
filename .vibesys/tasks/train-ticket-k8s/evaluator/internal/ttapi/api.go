package ttapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Info is the workload's description of one operation (loadgen's Request info). The oracle reads
// it back from the history, so the accuracy workload must emit exactly this.
//
//	Op:    create | update | upsert | delete | get | list | query
//	Table: config | station | train | route | price | travel
//	Key:   the id for get/delete/join (config: name; travel: "G1234")
//	Entity: the stored form of the entity a write carries (route: stations/distances arrays;
//	       travel: tripId {type,number}). For an upsert whose id the server generates (route,
//	       price) the id is a short non-empty placeholder (under 10 characters).
//	Q:     the query for Op "query":
//	       stationName(Key=id) stationId(A=name) priceBy(A=routeId, B=trainType)
//	       routeBetween(A=start, B=terminal) tripsByRoute(A=routeId) tripRoute(Key) tripTrain(Key)
type Info struct {
	Table  string          `json:"table"`
	Op     string          `json:"op"`
	Key    string          `json:"key,omitempty"`
	Entity json.RawMessage `json:"entity,omitempty"`
	Q      string          `json:"q,omitempty"`
	A      string          `json:"a,omitempty"`
	B      string          `json:"b,omitempty"`
}

// OpName is the operation name the workload gives loadgen ([a-z][a-z0-9_]*): "station_create",
// "route_query_route_between".
func (i Info) OpName() string {
	if i.Op == "query" {
		return i.Table + "_query_" + snake(i.Q)
	}
	return i.Table + "_" + i.Op
}

func snake(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

var Tables = []string{"config", "station", "train", "route", "price", "travel"}

// Generated: the server picks the id (route and price POST with an id under 10 characters).
func (i Info) Generated(entity string) bool {
	return i.Op == "upsert" && len(StrField(entity, "id")) < 10
}

// Out is a parsed response. Bad != "" is a schema/envelope violation.
type Out struct {
	Status int
	Ents   []string // canonical entities, sorted by key
	Val    string   // station id or name; "empty" for a join whose route is gone
	Bad    string
}

var prefix = map[string]string{
	"config": "/api/v1/configservice", "station": "/api/v1/stationservice", "train": "/api/v1/trainservice",
	"travel": "/api/v1/travelservice", "route": "/api/v1/routeservice", "price": "/api/v1/priceservice",
}
var coll = map[string]string{
	"config": "/configs", "station": "/stations", "train": "/trains",
	"travel": "/trips", "route": "/routes", "price": "/prices",
}

// QueryTable is the table a query reads.
func QueryTable(q string) string {
	switch q {
	case "stationId", "stationName":
		return "station"
	case "priceBy":
		return "price"
	case "routeBetween":
		return "route"
	}
	return "travel"
}

func IsFind1(q string) bool { return q == "stationId" || q == "priceBy" }
func IsSet(q string) bool   { return q == "routeBetween" || q == "tripsByRoute" }
func IsJoin(q string) bool  { return q == "tripRoute" || q == "tripTrain" }

// bodyOf decodes an entity for a request body with its numbers as they are written: an integer
// stays an int64 (a float64 would go out as 1600000000000.0, which Jackson refuses for a Date or
// an int), anything else is a float64.
func bodyOf(entity json.RawMessage) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(entity))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch v := v.(type) {
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n, nil
			}
			return v.Float64()
		case map[string]any:
			for k, e := range v {
				w, err := walk(e)
				if err != nil {
					return nil, err
				}
				v[k] = w
			}
		case []any:
			for k, e := range v {
				w, err := walk(e)
				if err != nil {
					return nil, err
				}
				v[k] = w
			}
		}
		return v, nil
	}
	_, err := walk(m)
	return m, err
}

// Request maps an operation to the HTTP request of the real API.
func Request(i Info) (method, path string, body any, err error) {
	esc := url.PathEscape
	p := prefix[i.Table] + coll[i.Table]
	var m map[string]any
	if len(i.Entity) > 0 {
		if m, err = bodyOf(i.Entity); err != nil {
			return "", "", nil, err
		}
		if i.Table == "travel" { // TravelInfo.tripId is the string "G1234"; the stored Trip has {type,number}
			t := m["tripId"].(map[string]any)
			m["tripId"] = t["type"].(string) + t["number"].(string)
		}
		if i.Table == "price" && i.Op == "upsert" && len(m["id"].(string)) < 10 {
			delete(m, "id") // PriceConfig.id is a java UUID: "generate" means omit it
		}
	}
	switch i.Op {
	case "create":
		return http.MethodPost, p, m, nil
	case "update":
		return http.MethodPut, p, m, nil
	case "upsert":
		if i.Table == "route" { // RouteInfo, not the stored entity
			sl, dl := []string{}, []string{}
			for _, s := range m["stations"].([]any) {
				sl = append(sl, s.(string))
			}
			for _, d := range m["distances"].([]any) {
				dl = append(dl, fmt.Sprint(d))
			}
			return http.MethodPost, p, map[string]any{"id": m["id"], "startStation": m["startStationId"], "endStation": m["terminalStationId"],
				"stationList": strings.Join(sl, ","), "distanceList": strings.Join(dl, ",")}, nil
		}
		return http.MethodPost, p, m, nil
	case "delete":
		if i.Table == "station" || i.Table == "price" {
			return http.MethodDelete, p, m, nil
		}
		return http.MethodDelete, p + "/" + esc(i.Key), nil, nil
	case "get":
		return http.MethodGet, p + "/" + esc(i.Key), nil, nil
	case "list":
		return http.MethodGet, p, nil, nil
	case "query":
		switch i.Q {
		case "stationName":
			return http.MethodGet, prefix["station"] + "/stations/name/" + esc(i.Key), nil, nil
		case "stationId":
			return http.MethodGet, prefix["station"] + "/stations/id/" + esc(i.A), nil, nil
		case "priceBy":
			return http.MethodGet, prefix["price"] + "/prices/" + esc(i.A) + "/" + esc(i.B), nil, nil
		case "routeBetween":
			return http.MethodGet, prefix["route"] + "/routes/" + esc(i.A) + "/" + esc(i.B), nil, nil
		case "tripsByRoute":
			return http.MethodPost, prefix["travel"] + "/trips/routes", []string{i.A}, nil
		case "tripRoute":
			return http.MethodGet, prefix["travel"] + "/routes/" + esc(i.Key), nil, nil
		case "tripTrain":
			return http.MethodGet, prefix["travel"] + "/train_types/" + esc(i.Key), nil, nil
		}
	}
	return "", "", nil, fmt.Errorf("no request for %+v", i)
}

// ExpectHTTP is the success status each operation answers (the suite's: creates answer 201 except
// train and route).
func ExpectHTTP(i Info) int {
	if i.Op == "create" && i.Table != "train" && i.Table != "route" || i.Op == "upsert" && i.Table == "price" {
		return 201
	}
	return 200
}

type envelope struct {
	Status *int            `json:"status"`
	Msg    *string         `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// Parse maps (operation, HTTP status, body) to an Out, validating the envelope and every returned
// entity strictly. The caller has already routed non-2xx statuses (4xx violation, 5xx unknown).
func Parse(i Info, httpStatus int, body []byte) (out Out) {
	bad := func(f string, a ...any) Out { return Out{Bad: fmt.Sprintf(f, a...)} }
	if httpStatus != ExpectHTTP(i) {
		return bad("http status %d, want %d", httpStatus, ExpectHTTP(i))
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Status == nil || env.Msg == nil {
		return bad("malformed envelope")
	}
	if *env.Status != 0 && *env.Status != 1 {
		return bad("status %d", *env.Status)
	}
	out.Status = *env.Status
	isNull := len(env.Data) == 0 || string(env.Data) == "null"
	ent := func(table string, raw []byte) bool {
		_, c, err := Canon(table, raw)
		if err != nil {
			out.Bad = err.Error()
			return false
		}
		out.Ents = append(out.Ents, c)
		return true
	}
	sortEnts := func(table string) {
		sort.Slice(out.Ents, func(a, b int) bool { return KeyOf(table, out.Ents[a]) < KeyOf(table, out.Ents[b]) })
	}
	items := func(table string, list []json.RawMessage) bool {
		seen := map[string]bool{}
		for _, it := range list {
			if !ent(table, it) {
				return false
			}
			k := KeyOf(table, out.Ents[len(out.Ents)-1])
			if seen[k] {
				out.Bad = "duplicate key " + k
				return false
			}
			seen[k] = true
		}
		sortEnts(table)
		return true
	}
	list := func(table string) {
		if isNull && out.Status == 0 {
			return
		}
		var l []json.RawMessage
		if err := json.Unmarshal(env.Data, &l); err != nil {
			out.Bad = "data is not a list"
			return
		}
		if (len(l) == 0) != (out.Status == 0) {
			out.Bad = fmt.Sprintf("status %d with %d items", out.Status, len(l))
			return
		}
		items(table, l)
	}
	switch i.Op {
	case "create", "update", "delete":
	case "upsert":
		if out.Status != 1 || isNull {
			return bad("upsert without entity")
		}
		ent(i.Table, env.Data)
	case "get":
		if out.Status == 1 {
			if isNull {
				return bad("get status 1 without data")
			}
			ent(i.Table, env.Data)
		}
	case "list":
		list(i.Table)
	case "query":
		t := QueryTable(i.Q)
		switch {
		case i.Q == "stationName" || i.Q == "stationId":
			if out.Status == 1 {
				var s string
				if json.Unmarshal(env.Data, &s) != nil || s == "" {
					return bad("data is not a string")
				}
				out.Val = s
			}
		case i.Q == "priceBy":
			if out.Status == 1 {
				ent(t, env.Data)
			}
		case i.Q == "routeBetween":
			list("route")
		case i.Q == "tripsByRoute": // data: [[trip, ...]], one inner list per requested route id
			var outer [][]json.RawMessage
			if json.Unmarshal(env.Data, &outer) != nil || len(outer) != 1 {
				return bad("tripsByRoute: bad shape")
			}
			items("travel", outer[0])
			out.Status = 1 // the inner list may be empty
		case i.Q == "tripRoute":
			if out.Status == 1 {
				// TravelServiceImpl.getRouteByRouteId returns an EMPTY Route (all fields null) when
				// the route service has no such route, and the join still answers status 1.
				var probe map[string]any
				if json.Unmarshal(env.Data, &probe) == nil && probe["id"] == nil && probe["stations"] == nil {
					out.Val = "empty"
					return out
				}
				c, err := CanonJoinRoute(env.Data)
				if err != nil {
					return bad("%s", err.Error())
				}
				out.Ents = append(out.Ents, c)
			}
		case i.Q == "tripTrain":
			if out.Status == 1 {
				ent("train", env.Data)
			}
		}
	}
	return out
}

// Matches says whether a stored entity of the query's table satisfies the query's predicate.
func Matches(q, a, b, ent string) bool {
	switch q {
	case "":
		return true
	case "stationId":
		return StrField(ent, "name") == a
	case "priceBy":
		return StrField(ent, "routeId") == a && StrField(ent, "trainType") == b
	case "routeBetween":
		st := StrList(ent, "stations")
		ai, bi := -1, -1
		for i, x := range st {
			if x == a && ai < 0 {
				ai = i
			}
			if x == b && bi < 0 {
				bi = i
			}
		}
		// Java: contains(a) && contains(b) && indexOf(a) < indexOf(b)
		return ai >= 0 && bi >= 0 && ai < bi
	case "tripsByRoute":
		return StrField(ent, "routeId") == a
	}
	return false
}
