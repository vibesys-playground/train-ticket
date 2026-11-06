package ttworkload

import (
	"encoding/json"
	"maps"
	"net/url"
	"strings"
	"testing"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/workload"

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

// build builds the named operation on record item (drawing, on copies of m, until the stream
// picks it): only that build changes m.
func build(t *testing.T, m *Model, name string, item int) workload.Request {
	t.Helper()
	op := operation(t, name)
	for seed := uint64(0); seed < 1000; seed++ {
		trial := &Model{data: m.data, auth: m.auth, current: maps.Clone(m.current)}
		request := op.Build(trial, workload.NewRand(seed, "test"))
		info := map[string]any{}
		raw, _ := json.Marshal(request.Info)
		_ = json.Unmarshal(raw, &info)
		if int(info["item"].(float64)) == item {
			return op.Build(m, workload.NewRand(seed, "test"))
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

func steps(request workload.Request) []string {
	out := make([]string, len(request.Steps))
	for i, s := range request.Steps {
		out[i] = s.String()
	}
	return out
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
	}
	if sums["list"] != 35 || sums["read"] != 25 || sums["update"] != 35 {
		t.Fatalf("weights %v, want list 35, read 25, update 35", sums)
	}
}

func TestRequestsHitTheServicesRoutes(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[2]
	// Each case builds on the fixture: an update changes the model the next request builds on.
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
		request := build(t, newModel(t), name, 2)
		if got := strings.Join(steps(request), " "); got != strings.Join(want, " ") {
			t.Errorf("%s:\n got %s\nwant %s", name, got, strings.Join(want, " "))
		}
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

func TestUpdatesBuildOnTheLatestValue(t *testing.T) {
	m := newModel(t)
	r := m.data.Records[1]
	// A route flips direction with each update; its distance grows.
	first := build(t, m, "update_read_route", 1)
	if m.Latest(Key{ttdata.Route, 1}) != mustValue(t, first) {
		t.Fatal("the model does not hold the update's value")
	}
	flipped := decode[ttdata.RouteEntity](m.Latest(Key{ttdata.Route, 1}))
	if flipped.StartStationID != r.StationB.ID || flipped.TerminalStationID != r.StationA.ID || flipped.Distances[1] <= r.Route.Distances[1] {
		t.Fatalf("first update: %+v", flipped)
	}
	second := build(t, m, "update_read_route", 1)
	if got := steps(second)[2]; got != "Step(GET /api/v1/routeservice/routes/"+r.StationB.ID+"/"+r.StationA.ID+")" {
		t.Fatalf("second update retires %s", got)
	}
	if back := decode[ttdata.RouteEntity](m.Latest(Key{ttdata.Route, 1})); back.StartStationID != r.StationA.ID {
		t.Fatalf("second update: %+v", back)
	}
	// A price moves between its route and the next record's, keeping its train.
	build(t, m, "update_read_price", 1)
	moved := decode[ttdata.PriceEntity](m.Latest(Key{ttdata.Price, 1}))
	if moved.RouteID != m.data.Records[2].Route.ID || moved.TrainType != r.Train.ID || moved.ID != r.Price.ID {
		t.Fatalf("price update: %+v", moved)
	}
	if got := steps(build(t, m, "read_price", 1))[0]; !strings.Contains(got, m.data.Records[2].Route.ID) {
		t.Fatalf("a read after the update reads %s", got)
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
