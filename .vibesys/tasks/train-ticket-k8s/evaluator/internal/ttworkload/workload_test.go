package ttworkload

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

func newModel(t *testing.T) *Model {
	t.Helper()
	m, err := NewModel(ttdata.Generate(11, 4), workload.NewRand(5, "model"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func operation(t *testing.T, name string) workload.Operation[*Model] {
	t.Helper()
	for _, op := range Workload().Operations {
		if op.Name == name {
			return op
		}
	}
	t.Fatalf("no operation %s", name)
	return workload.Operation[*Model]{}
}

// build builds the named operation on record item (drawing until the stream picks it).
func build(t *testing.T, m *Model, name string, item int) workload.Request {
	t.Helper()
	op := operation(t, name)
	for seed := uint64(0); seed < 1000; seed++ {
		request := op.Build(m, workload.NewRand(seed, "test"))
		info := map[string]any{}
		raw, _ := json.Marshal(request.Info)
		_ = json.Unmarshal(raw, &info)
		if int(info["item"].(float64)) == item {
			return request
		}
	}
	t.Fatalf("%s never drew record %d", name, item)
	return workload.Request{}
}

func info(t *testing.T, request workload.Request) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(request.Info)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var t0 = time.Unix(1_800_000_000, 0)

func at(secs float64) time.Time { return t0.Add(time.Duration(secs * float64(time.Second))) }

func body(status int, data any) []byte {
	encoded, _ := json.Marshal(map[string]any{"status": status, "msg": "m", "data": data})
	return encoded
}

func call(request json.RawMessage, start, end float64, bodies ...[]byte) workload.Call {
	responses := make([]workload.Response, len(bodies))
	for i, b := range bodies {
		responses[i] = workload.Response{Status: 200, Body: b}
	}
	return workload.Call{Info: request, Start: at(start), End: at(end), OK: true, Responses: responses}
}

func steps(request workload.Request) []string {
	out := make([]string, len(request.Steps))
	for i, s := range request.Steps {
		out[i] = s.String()
	}
	return out
}

// entity is the record's entity of service as the service returns it (generic JSON).
func entity(value any) any {
	return normalized(canonical(value))
}

func TestTheMixIsServicebenchsWithoutDelete(t *testing.T) {
	ops := Workload().Operations
	if len(ops) != 18 {
		t.Fatalf("%d operations, want 18", len(ops))
	}
	sums := map[string]float64{}
	for _, op := range ops {
		kind := op.Name[:strings.Index(op.Name, "_")]
		sums[kind] += op.Weight
		if (op.Apply != nil) != (kind == "update") || op.Check == nil {
			t.Errorf("%s: write %v, check %v", op.Name, op.Apply != nil, op.Check != nil)
		}
		if op.Sample != 0 {
			t.Errorf("%s: sample %v, want the default", op.Name, op.Sample)
		}
	}
	if sums["list"] != 35 || sums["read"] != 25 || sums["update"] != 35 {
		t.Fatalf("weights %v, want list 35, read 25, update 35", sums)
	}
}

func TestRequestsHitTheServicesRoutes(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[2]
	cases := map[string][]string{
		"list_price":   {"Step(GET /api/v1/priceservice/prices)"},
		"read_config":  {"Step(GET /api/v1/configservice/configs/" + url.PathEscape(r.Config.Name) + ")"},
		"read_station": {"Step(GET /api/v1/stationservice/stations/name/" + r.StationA.ID + ")"},
		"read_train":   {"Step(GET /api/v1/trainservice/trains/" + r.Train.ID + ")"},
		"read_travel":  {"Step(GET /api/v1/travelservice/trips/" + r.Trip.TripID.String() + ")"},
		"read_route":   {"Step(GET /api/v1/routeservice/routes/" + r.Route.ID + ")"},
		"read_price":   {"Step(GET /api/v1/priceservice/prices/" + r.Route.ID + "/" + r.Train.ID + ")"},
		"update_read_config": {"Step(PUT /api/v1/configservice/configs)",
			"Step(GET /api/v1/configservice/configs/" + url.PathEscape(r.Config.Name) + ")"},
		"update_read_station": {"Step(PUT /api/v1/stationservice/stations)",
			"Step(GET /api/v1/stationservice/stations/name/" + r.StationA.ID + ")",
			"Step(GET /api/v1/stationservice/stations/id/" + url.PathEscape(r.StationA.Name) + ")"},
		"update_read_route": {"Step(POST /api/v1/routeservice/routes)",
			"Step(GET /api/v1/routeservice/routes/" + r.Route.ID + ")",
			"Step(GET /api/v1/routeservice/routes/" + r.StationA.ID + "/" + r.StationB.ID + ")"},
		"update_read_price": {"Step(PUT /api/v1/priceservice/prices)",
			"Step(GET /api/v1/priceservice/prices/" + m.data.Records[3].Route.ID + "/" + r.Train.ID + ")",
			"Step(GET /api/v1/priceservice/prices/" + r.Route.ID + "/" + r.Train.ID + ")"},
		"update_read_travel": {"Step(PUT /api/v1/travelservice/trips)",
			"Step(GET /api/v1/travelservice/trips/" + r.Trip.TripID.String() + ")"},
	}
	for name, want := range cases {
		request := build(t, m, name, 2)
		if got := strings.Join(steps(request), " "); got != strings.Join(want, " ") {
			t.Errorf("%s:\n got %s\nwant %s", name, got, strings.Join(want, " "))
		}
		if strings.HasPrefix(name, "update_") != (request.Exclusive != "") {
			t.Errorf("%s: exclusive key %q", name, request.Exclusive)
		}
	}
	if got := build(t, m, "update_read_route", 2).Exclusive; got != "route/2" {
		t.Errorf("route update holds %q, want route/2", got)
	}
}

func TestBuildIsAFunctionOfModelAndStream(t *testing.T) {
	for _, op := range Workload().Operations {
		a := op.Build(newModel(t), workload.NewRand(9, "x"))
		b := op.Build(newModel(t), workload.NewRand(9, "x"))
		if strings.Join(steps(a), " ") != strings.Join(steps(b), " ") || string(info(t, a)) != string(info(t, b)) {
			t.Errorf("%s: two builds differ", op.Name)
		}
	}
}

// apply records request's update in the model as acknowledged over [start, end].
func apply(t *testing.T, m *Model, name string, request workload.Request, start, end float64) {
	t.Helper()
	c := workload.Call{Info: info(t, request), Start: at(start), End: at(end), OK: true}
	if err := operation(t, name).Apply(m, c); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatesBuildOnTheLatestValue(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[1]
	// A route flips direction with each update; its distance grows.
	first := build(t, m, "update_read_route", 1)
	m.versions.Write(Key{ttdata.Route, 1}, mustValue(t, first), workload.Call{Pending: true})
	flipped := decode[ttdata.RouteEntity](m.Latest(Key{ttdata.Route, 1}))
	if flipped.StartStationID != r.StationB.ID || flipped.TerminalStationID != r.StationA.ID || flipped.Distances[1] <= r.Route.Distances[1] {
		t.Fatalf("first update: %+v", flipped)
	}
	second := build(t, m, "update_read_route", 1)
	if got := steps(second)[2]; got != "Step(GET /api/v1/routeservice/routes/"+r.StationB.ID+"/"+r.StationA.ID+")" {
		t.Fatalf("second update retires %s", got)
	}
	m.versions.Write(Key{ttdata.Route, 1}, mustValue(t, second), workload.Call{Pending: true})
	if back := decode[ttdata.RouteEntity](m.Latest(Key{ttdata.Route, 1})); back.StartStationID != r.StationA.ID {
		t.Fatalf("second update: %+v", back)
	}
	// A price moves between its route and the next record's, keeping its train.
	price := build(t, m, "update_read_price", 1)
	m.versions.Write(Key{ttdata.Price, 1}, mustValue(t, price), workload.Call{Pending: true})
	moved := decode[ttdata.PriceEntity](m.Latest(Key{ttdata.Price, 1}))
	if moved.RouteID != m.data.Records[2].Route.ID || moved.TrainType != r.Train.ID || moved.ID != r.Price.ID {
		t.Fatalf("price update: %+v", moved)
	}
	if got := steps(build(t, m, "read_price", 1))[0]; !strings.Contains(got, m.data.Records[2].Route.ID) {
		t.Fatalf("a read after the pending update reads %s", got)
	}
	back := build(t, m, "update_read_price", 1)
	if v := decode[ttdata.PriceEntity](mustValue(t, back)); v.RouteID != r.Route.ID {
		t.Fatalf("price moves back to %s, want %s", v.RouteID, r.Route.ID)
	}
	// A train's economy class grows by 7 per update.
	train := build(t, m, "update_read_train", 1)
	if v := decode[ttdata.TrainEntity](mustValue(t, train)); v.EconomyClass != r.Train.EconomyClass+7 || v.ID != r.Train.ID {
		t.Fatalf("train update: %+v", v)
	}
}

func mustValue(t *testing.T, request workload.Request) string {
	t.Helper()
	var u UpdateInfo
	if err := json.Unmarshal(info(t, request), &u); err != nil {
		t.Fatal(err)
	}
	return string(u.Value)
}

func TestListCheck(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[0]
	request := build(t, m, "list_station", 0)
	check := operation(t, "list_station").Check
	all := func(first any) []any {
		items := []any{first}
		for _, other := range m.data.Records[1:] {
			items = append(items, entity(other.StationA), entity(other.StationB))
		}
		return items
	}
	if err := check(m, call(info(t, request), 1, 2, body(1, all(entity(r.StationA))))); err != nil {
		t.Fatalf("a good list failed: %v", err)
	}
	missing := all(entity(m.data.Records[0].StationB))
	if err := check(m, call(info(t, request), 1, 2, body(1, missing))); err == nil || !strings.Contains(err.Error(), "omits") {
		t.Fatalf("a list without the record passed: %v", err)
	}
	extra := entity(r.StationA).(map[string]any)
	extra["city"] = "x"
	if err := check(m, call(info(t, request), 1, 2, body(1, all(extra)))); err == nil || !strings.Contains(err.Error(), "fields") {
		t.Fatalf("an extra field passed: %v", err)
	}
	if err := check(m, call(info(t, request), 1, 2, body(1, append(all(entity(r.StationA)), entity(r.StationA))))); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a duplicate passed: %v", err)
	}
	if err := check(m, call(info(t, request), 1, 2, body(0, nil))); err == nil {
		t.Fatal("status 0 passed")
	}
	// After a rename acknowledged at 3, a list started at 5 must show the new name; one that
	// overlaps the rename may show either.
	update := build(t, m, "update_read_station", 0)
	apply(t, m, "update_read_station", update, 3, 4)
	renamed := normalized(mustValue(t, update))
	if err := check(m, call(info(t, request), 5, 6, body(1, all(entity(r.StationA))))); err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("a stale list passed: %v", err)
	}
	for _, shown := range []any{entity(r.StationA), renamed} {
		if err := check(m, call(info(t, request), 3.5, 3.8, body(1, all(shown)))); err != nil {
			t.Fatalf("an overlapping list failed: %v", err)
		}
	}
	if err := check(m, call(info(t, request), 5, 6, body(1, all(renamed)))); err != nil {
		t.Fatalf("a fresh list failed: %v", err)
	}
}

func TestReadChecks(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[3]
	config := build(t, m, "read_config", 3)
	if err := operation(t, "read_config").Check(m, call(info(t, config), 1, 2, body(1, entity(r.Config)))); err != nil {
		t.Fatal(err)
	}
	wrong := r.Config
	wrong.Value = "v-other"
	if err := operation(t, "read_config").Check(m, call(info(t, config), 1, 2, body(1, entity(wrong)))); err == nil {
		t.Fatal("a wrong value passed")
	}
	station := build(t, m, "read_station", 3)
	if err := operation(t, "read_station").Check(m, call(info(t, station), 1, 2, body(1, r.StationA.Name))); err != nil {
		t.Fatal(err)
	}
	if err := operation(t, "read_station").Check(m, call(info(t, station), 1, 2, body(1, r.StationB.Name))); err == nil {
		t.Fatal("another station's name passed")
	}
	// A read of a price by its route and train: found while it is there; once a move was
	// acknowledged, not found; around the move, either.
	price := build(t, m, "read_price", 3)
	check := operation(t, "read_price").Check
	if err := check(m, call(info(t, price), 1, 2, body(1, entity(r.Price)))); err != nil {
		t.Fatal(err)
	}
	if err := check(m, call(info(t, price), 1, 2, body(0, nil))); err == nil {
		t.Fatal("a price that never moved was not found, and that passed")
	}
	move := build(t, m, "update_read_price", 3)
	apply(t, m, "update_read_price", move, 3, 4)
	if err := check(m, call(info(t, price), 5, 6, body(0, nil))); err != nil {
		t.Fatalf("not found after the move failed: %v", err)
	}
	if err := check(m, call(info(t, price), 5, 6, body(1, entity(r.Price)))); err == nil {
		t.Fatal("the moved price at its old key passed")
	}
	if err := check(m, call(info(t, price), 3.2, 3.4, body(1, entity(r.Price)))); err != nil {
		t.Fatalf("the old price during the move failed: %v", err)
	}
}

func TestUpdateChecks(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[2]
	request := build(t, m, "update_read_station", 2)
	value := decode[ttdata.StationEntity](mustValue(t, request))
	check := operation(t, "update_read_station").Check
	good := call(info(t, request), 1, 2, body(1, value), body(1, value.Name), body(0, r.StationA.Name))
	if err := check(m, good); err != nil {
		t.Fatal(err)
	}
	stale := call(info(t, request), 1, 2, body(1, value), body(1, r.StationA.Name), body(0, r.StationA.Name))
	if err := check(m, stale); err == nil || !strings.Contains(err.Error(), "read your write") {
		t.Fatalf("a stale read passed: %v", err)
	}
	retired := call(info(t, request), 1, 2, body(1, value), body(1, value.Name), body(1, r.StationA.ID))
	if err := check(m, retired); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("the retired name still resolving passed: %v", err)
	}
	if err := check(m, call(info(t, request), 1, 2, body(0, nil), body(1, value.Name), body(0, nil))); err == nil {
		t.Fatal("a refused update passed")
	}
	if err := check(m, call(info(t, request), 1, 2, body(1, value), body(1, value.Name))); err == nil {
		t.Fatal("a missing step passed")
	}
	trip := build(t, m, "update_read_travel", 2)
	tripValue := normalized(mustValue(t, trip))
	if err := operation(t, "update_read_travel").Check(m, call(info(t, trip), 1, 2, body(1, nil), body(1, tripValue))); err != nil {
		t.Fatal(err)
	}
	old := entity(r.Trip)
	if err := operation(t, "update_read_travel").Check(m, call(info(t, trip), 1, 2, body(1, nil), body(1, old))); err == nil {
		t.Fatal("the old trip after its update passed")
	}
}

func TestEnvelopeIsStrict(t *testing.T) {
	c := workload.Call{Responses: []workload.Response{{Status: 200, Body: []byte(`{"status":1,"msg":"m","data":1,"extra":2}`)}}}
	if _, err := envelope(c, 0, 1); err == nil {
		t.Fatal("an extra envelope field passed")
	}
	c.Responses[0].Body = []byte(`{"status":1,"msg":3,"data":1}`)
	if _, err := envelope(c, 0, 1); err == nil {
		t.Fatal("a numeric msg passed")
	}
	c.Responses[0].Body = []byte(`not json`)
	if _, err := envelope(c, 0, 1); err == nil {
		t.Fatal("a non-JSON body passed")
	}
}
