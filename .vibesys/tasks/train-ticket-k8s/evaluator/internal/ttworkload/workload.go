// Package ttworkload is the train-ticket-k8s benchmark's workload on the vibesys-evaluator Go
// SDK: the operations of the mix with their weights, how each request is drawn, how a write
// changes the model, and how each response is checked after the phase. cmd/generator runs it
// as the generator that loadgen reads the request stream from (bench.py names it).
//
// It ports the servicebench Train Ticket benchmark adapter (vibesys
// resources/evaluators/microservice/apps/trainticket: application.go, model.go,
// operations.go, validation.go), whose engine held a lock per fixture record so that a read
// never overlapped an update of its record. Here an update holds its entity's exclusive key
// (updates of one entity run one at a time, in stream order) and reads run freely: a read is
// checked against every value the visibility rule allows (Versions, with Bound).
//
// Operations, each on a fixture record drawn uniformly (weights out of 95):
//
//   - list_<service> (35): the collection; every element has the service's exact field set
//     and types, keys are unique, and the drawn record's entity is present with a value the
//     rule allows;
//   - read_<service> (25): the drawn entity by its key (a station's name by its id, a price
//     by its route and train), exact;
//   - update_read_<service> (35): a full update of the drawn entity, then a read that must
//     return exactly the written value (read your write), then, where the update moved a
//     secondary key (a station's name, a route's direction, a price's route), a read by the
//     retired key that must find nothing.
//
// The servicebench mix also had create_read_delete_config (weight 5): it needs an HTTP
// DELETE, which the Go SDK cannot send yet (it has Get, Post and Put; the schema and loadgen
// already have DELETE). See the task's README.
package ttworkload

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// Bound is the visibility bound: a read that starts after a write was acknowledged must show
// it (or a later value). The servicebench adapter's per-record locks enforced the same.
const Bound = 0 * time.Second

// Key names one entity of the fixture: a service's entity of record Item.
type Key struct {
	Service string
	Item    int
}

// Exclusive is the key's exclusive key: updates of one entity never overlap.
func (k Key) Exclusive() string { return fmt.Sprintf("%s/%d", k.Service, k.Item) }

// Model is the fixture of a sample and every update of it: the dataset's entities and, per
// entity, the values written with their times (Versions holds them as canonical JSON).
type Model struct {
	data     *ttdata.Dataset
	auth     string
	initial  map[Key]string
	versions *workload.Versions[Key, string]
}

// NewModel is the model of a sample: rand is the sample's model stream, which names the
// sample's admin identity.
func NewModel(data *ttdata.Dataset, rand *workload.Rand) (*Model, error) {
	token := rand.Child("token")
	m := &Model{
		data:     data,
		auth:     "Bearer " + ttdata.AdminToken(token.Uint64(), token.Uint64()),
		initial:  map[Key]string{},
		versions: workload.NewVersions[Key, string](Bound),
	}
	for item, record := range data.Records {
		for _, service := range ttdata.Services {
			m.initial[Key{service, item}] = canonical(entityOf(service, record))
		}
	}
	return m, nil
}

// Authorization is the header every request of the sample carries.
func (m *Model) Authorization() string { return m.auth }

func entityOf(service string, record ttdata.Record) any {
	switch service {
	case ttdata.Config:
		return record.Config
	case ttdata.Station:
		return record.StationA
	case ttdata.Train:
		return record.Train
	case ttdata.Travel:
		return record.Trip
	case ttdata.Route:
		return record.Route
	case ttdata.Price:
		return record.Price
	}
	panic("unknown service " + service)
}

func canonical(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// Latest is the entity's value generation builds on (pending updates included).
func (m *Model) Latest(key Key) string { return m.versions.Latest(key, m.initial[key]) }

// Allowed is the values a read of the entity over [start, end] may show.
func (m *Model) Allowed(key Key, start, end time.Time) []string {
	return m.versions.Allowed(key, m.initial[key], start, end)
}

func decode[T any](text string) T {
	var value T
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		panic(fmt.Sprintf("decode %T from the model: %v", value, err))
	}
	return value
}

// Workload is the benchmark's workload.
func Workload() workload.Workload[*ttdata.Dataset, *Model] {
	ops := []workload.Operation[*Model]{}
	listWeights := map[string]float64{ttdata.Config: 6, ttdata.Station: 6, ttdata.Train: 6, ttdata.Travel: 6, ttdata.Route: 6, ttdata.Price: 5}
	readWeights := map[string]float64{ttdata.Config: 5, ttdata.Station: 4, ttdata.Train: 4, ttdata.Travel: 4, ttdata.Route: 4, ttdata.Price: 4}
	updateWeights := map[string]float64{ttdata.Config: 6, ttdata.Station: 6, ttdata.Train: 6, ttdata.Travel: 6, ttdata.Route: 6, ttdata.Price: 5}
	for _, service := range ttdata.Services {
		ops = append(ops, workload.Operation[*Model]{Name: "list_" + service, Weight: listWeights[service], Build: buildList(service), Check: checkList(service)})
	}
	for _, service := range ttdata.Services {
		ops = append(ops, workload.Operation[*Model]{Name: "read_" + service, Weight: readWeights[service], Build: buildRead(service), Check: checkRead(service)})
	}
	for _, service := range ttdata.Services {
		ops = append(ops, workload.Operation[*Model]{
			Name: "update_read_" + service, Weight: updateWeights[service],
			Build: buildUpdate(service), Check: checkUpdate(service), Apply: applyUpdate(service),
		})
	}
	return workload.Workload[*ttdata.Dataset, *Model]{Dataset: ttdata.Load, Model: NewModel, Operations: ops}
}

// ---------------------------------------------------------------- requests

// ItemInfo is a list's Info: the record whose entity the list must hold.
type ItemInfo struct {
	Item int `json:"item"`
}

// ReadInfo is a read's Info: the record and the path read (a price's path names the route and
// train it was built with).
type ReadInfo struct {
	Item int    `json:"item"`
	Path string `json:"path"`
}

// UpdateInfo is an update's Info: the record, the entity's new value, and the secondary key it
// retired (a station's old name, a route's old start and terminal, a price's old route and
// train), when the update has a negative read.
type UpdateInfo struct {
	Item    int             `json:"item"`
	Value   json.RawMessage `json:"value"`
	Retired string          `json:"retired,omitempty"`
}

func (m *Model) get(path string) workload.Step {
	return workload.Get(path, workload.Header("Authorization", m.auth))
}

func buildList(service string) func(*Model, *workload.Rand) workload.Request {
	return func(m *Model, rand *workload.Rand) workload.Request {
		item := rand.Below(len(m.data.Records))
		return workload.Request{Steps: []workload.Step{m.get(ttdata.Collection(service))}, Info: ItemInfo{item}}
	}
}

// ReadPath is the path that reads the entity whose value is current.
func ReadPath(service, current string) string {
	switch service {
	case ttdata.Config:
		return ttdata.Path(service, "/configs/"+url.PathEscape(decode[ttdata.ConfigEntity](current).Name))
	case ttdata.Station:
		return ttdata.Path(service, "/stations/name/"+url.PathEscape(decode[ttdata.StationEntity](current).ID))
	case ttdata.Train:
		return ttdata.Path(service, "/trains/"+url.PathEscape(decode[ttdata.TrainEntity](current).ID))
	case ttdata.Travel:
		return ttdata.Path(service, "/trips/"+url.PathEscape(decode[ttdata.TripEntity](current).TripID.String()))
	case ttdata.Route:
		return ttdata.Path(service, "/routes/"+url.PathEscape(decode[ttdata.RouteEntity](current).ID))
	case ttdata.Price:
		price := decode[ttdata.PriceEntity](current)
		return pricePath(price.RouteID, price.TrainType)
	}
	panic("unknown service " + service)
}

func pricePath(route, train string) string {
	return ttdata.Path(ttdata.Price, "/prices/"+url.PathEscape(route)+"/"+url.PathEscape(train))
}

func buildRead(service string) func(*Model, *workload.Rand) workload.Request {
	return func(m *Model, rand *workload.Rand) workload.Request {
		item := rand.Below(len(m.data.Records))
		path := ReadPath(service, m.Latest(Key{service, item}))
		return workload.Request{Steps: []workload.Step{m.get(path)}, Info: ReadInfo{item, path}}
	}
}

// Update is what an update of an entity sends and expects.
type Update struct {
	Value   any           // the entity's new value, as the service returns it
	Write   workload.Step // the update
	Retired string        // the retired secondary key, or ""
	Lookup  string        // the read by the retired key, or ""
}

// NewUpdate builds the update of record item's entity of service from its current value and
// the operation's version word (servicebench's updatedEntity).
func (m *Model) NewUpdate(service string, item int, current string, version uint64) Update {
	auth := workload.Header("Authorization", m.auth)
	record := m.data.Records[item]
	switch service {
	case ttdata.Config:
		updated := decode[ttdata.ConfigEntity](current)
		updated.Value = fmt.Sprintf("v-%016x", version)
		updated.Description = fmt.Sprintf("d-%016x", version^0xa5a5a5a5a5a5a5a5)
		return Update{Value: updated, Write: workload.Put(ttdata.Collection(service), workload.JSON(updated), auth)}
	case ttdata.Station:
		old := decode[ttdata.StationEntity](current)
		updated := old
		updated.Name = ttdata.UpdatedStationName(version)
		updated.StayTime = ttdata.UpdatedStationStayTime(version)
		return Update{
			Value: updated, Write: workload.Put(ttdata.Collection(service), workload.JSON(updated), auth),
			Retired: old.Name, Lookup: ttdata.Path(service, "/stations/id/"+url.PathEscape(old.Name)),
		}
	case ttdata.Train:
		updated := decode[ttdata.TrainEntity](current)
		updated.AverageSpeed = ttdata.UpdatedTrainSpeed(version)
		updated.EconomyClass = ttdata.UpdatedTrainEconomy(updated.EconomyClass)
		return Update{Value: updated, Write: workload.Put(ttdata.Collection(service), workload.JSON(updated), auth)}
	case ttdata.Route:
		old := decode[ttdata.RouteEntity](current)
		updated := old
		a, b := record.StationA.ID, record.StationB.ID
		if old.StartStationID == a {
			a, b = b, a
		}
		updated.Stations = []string{a, b}
		updated.StartStationID, updated.TerminalStationID = a, b
		updated.Distances = []int{0, ttdata.UpdatedRouteDistance(old.Distances[len(old.Distances)-1], version)}
		return Update{
			Value: updated, Write: workload.Post(ttdata.Collection(service), workload.JSON(updated.Input()), auth),
			Retired: old.StartStationID + "/" + old.TerminalStationID,
			Lookup:  ttdata.Path(service, "/routes/"+url.PathEscape(old.StartStationID)+"/"+url.PathEscape(old.TerminalStationID)),
		}
	case ttdata.Price:
		old := decode[ttdata.PriceEntity](current)
		updated := old
		alternate := m.data.Records[(item+1)%len(m.data.Records)].Route.ID
		if old.RouteID == alternate {
			updated.RouteID = record.Route.ID
		} else {
			updated.RouteID = alternate
		}
		updated.TrainType = record.Train.ID
		updated.BasicPriceRate, updated.FirstClassPriceRate = ttdata.UpdatedPriceRates(version)
		return Update{
			Value: updated, Write: workload.Put(ttdata.Collection(service), workload.JSON(updated), auth),
			Retired: old.RouteID + "/" + old.TrainType, Lookup: pricePath(old.RouteID, old.TrainType),
		}
	case ttdata.Travel:
		updated := decode[ttdata.TripEntity](current)
		updated.EndTime = ttdata.UpdatedTripEnd(updated.EndTime, version)
		return Update{Value: updated, Write: workload.Put(ttdata.Collection(service), workload.JSON(updated.Input()), auth)}
	}
	panic("unknown service " + service)
}

func buildUpdate(service string) func(*Model, *workload.Rand) workload.Request {
	return func(m *Model, rand *workload.Rand) workload.Request {
		item := rand.Below(len(m.data.Records))
		version := rand.Uint64()
		key := Key{service, item}
		update := m.NewUpdate(service, item, m.Latest(key), version)
		value := canonical(update.Value)
		steps := []workload.Step{update.Write, m.get(ReadPath(service, value))}
		if update.Lookup != "" {
			steps = append(steps, m.get(update.Lookup))
		}
		return workload.Request{
			Steps:     steps,
			Info:      UpdateInfo{Item: item, Value: json.RawMessage(value), Retired: update.Retired},
			Exclusive: key.Exclusive(),
		}
	}
}

func applyUpdate(service string) func(*Model, workload.Call) error {
	return func(m *Model, call workload.Call) error {
		var info UpdateInfo
		if err := call.DecodeInfo(&info); err != nil {
			return err
		}
		if info.Item < 0 || info.Item >= len(m.data.Records) {
			return fmt.Errorf("record %d is not in the fixture", info.Item)
		}
		value, err := canonicalOf(service, info.Value)
		if err != nil {
			return err
		}
		m.versions.Write(Key{service, info.Item}, value, call)
		return nil
	}
}

// canonicalOf re-encodes a service's entity in its canonical form.
func canonicalOf(service string, raw json.RawMessage) (string, error) {
	var value any
	switch service {
	case ttdata.Config:
		value = new(ttdata.ConfigEntity)
	case ttdata.Station:
		value = new(ttdata.StationEntity)
	case ttdata.Train:
		value = new(ttdata.TrainEntity)
	case ttdata.Travel:
		value = new(ttdata.TripEntity)
	case ttdata.Route:
		value = new(ttdata.RouteEntity)
	case ttdata.Price:
		value = new(ttdata.PriceEntity)
	default:
		return "", fmt.Errorf("unknown service %q", service)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return "", fmt.Errorf("%s value %s: %w", service, raw, err)
	}
	return canonical(value), nil
}

// ---------------------------------------------------------------- checks

func checkList(service string) func(*Model, workload.Call) error {
	return func(m *Model, call workload.Call) error {
		var info ItemInfo
		if err := call.DecodeInfo(&info); err != nil {
			return err
		}
		data, err := envelope(call, 0, 1)
		if err != nil {
			return err
		}
		items, ok := data.([]any)
		if !ok || len(items) == 0 {
			return fmt.Errorf("%s list: data must be a non-empty array, got %s", service, brief(data))
		}
		key := Key{service, info.Item}
		want, err := entityKey(service, normalized(m.initial[key]).(map[string]any))
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(items))
		var found any
		for index, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("%s list item %d is not an object: %s", service, index, brief(item))
			}
			if err := validateEntity(service, object); err != nil {
				return fmt.Errorf("%s list item %d: %w", service, index, err)
			}
			id, _ := entityKey(service, object)
			if seen[id] {
				return fmt.Errorf("%s list holds key %q twice", service, id)
			}
			seen[id] = true
			if id == want {
				found = object
			}
		}
		if found == nil {
			return fmt.Errorf("%s list omits record %d's entity %q", service, info.Item, want)
		}
		return oneOf(fmt.Sprintf("%s list: record %d's entity", service, info.Item), found, m.Allowed(key, call.Start, call.End))
	}
}

func checkRead(service string) func(*Model, workload.Call) error {
	return func(m *Model, call workload.Call) error {
		var info ReadInfo
		if err := call.DecodeInfo(&info); err != nil {
			return err
		}
		key := Key{service, info.Item}
		allowed := m.Allowed(key, call.Start, call.End)
		what := fmt.Sprintf("read %s (record %d)", info.Path, info.Item)
		if service == ttdata.Price {
			return checkPriceRead(what, call, info.Path, allowed)
		}
		data, err := envelope(call, 0, 1)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if service == ttdata.Station {
			names := make([]string, len(allowed))
			for i, value := range allowed {
				names[i] = canonical(decode[ttdata.StationEntity](value).Name)
			}
			allowed = names
		}
		return oneOf(what, data, allowed)
	}
}

// checkPriceRead checks a read of a price by route and train: a value the rule allows that
// has that route and train is found exactly; one that has moved away is not found.
func checkPriceRead(what string, call workload.Call, path string, allowed []string) error {
	var problems []string
	for _, value := range allowed {
		price := decode[ttdata.PriceEntity](value)
		if pricePath(price.RouteID, price.TrainType) == path {
			data, err := envelope(call, 0, 1)
			if err == nil {
				err = oneOf(what, data, []string{value})
			}
			if err == nil {
				return nil
			}
			problems = append(problems, err.Error())
		} else {
			if _, err := envelope(call, 0, 0); err == nil {
				return nil
			} else {
				problems = append(problems, fmt.Sprintf("price moved to %s/%s: want not found: %v", price.RouteID, price.TrainType, err))
			}
		}
	}
	return fmt.Errorf("%s: no allowed value explains the response: %s", what, strings.Join(problems, "; "))
}

func checkUpdate(service string) func(*Model, workload.Call) error {
	return func(m *Model, call workload.Call) error {
		var info UpdateInfo
		if err := call.DecodeInfo(&info); err != nil {
			return err
		}
		value, err := canonicalOf(service, info.Value)
		if err != nil {
			return err
		}
		steps := 2
		if info.Retired != "" {
			steps = 3
		}
		if len(call.Responses) != steps {
			return fmt.Errorf("update of record %d: %d responses, want %d", info.Item, len(call.Responses), steps)
		}
		if _, err := envelope(call, 0, 1); err != nil {
			return fmt.Errorf("update of record %d: step 1: %w", info.Item, err)
		}
		data, err := envelope(call, 1, 1)
		if err != nil {
			return fmt.Errorf("update of record %d: step 2 (read your write): %w", info.Item, err)
		}
		want := value
		if service == ttdata.Station {
			want = canonical(decode[ttdata.StationEntity](value).Name)
		}
		if err := oneOf(fmt.Sprintf("update of record %d: step 2 (read your write)", info.Item), data, []string{want}); err != nil {
			return err
		}
		if info.Retired != "" {
			if _, err := envelope(call, 2, 0); err != nil {
				return fmt.Errorf("update of record %d: step 3 (the retired key %q must find nothing): %w", info.Item, info.Retired, err)
			}
		}
		return nil
	}
}

// envelope checks step's response is the services' envelope {status, msg, data} with the
// application status want, and returns its data.
func envelope(call workload.Call, step, want int) (any, error) {
	if step >= len(call.Responses) {
		return nil, fmt.Errorf("no response to step %d", step+1)
	}
	response := call.Responses[step]
	var body map[string]any
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return nil, fmt.Errorf("HTTP %d body is not a JSON object: %v (%s)", response.Status, err, brief(string(response.Body)))
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"data", "msg", "status"}) {
		return nil, fmt.Errorf("envelope fields %v, want [data msg status]", keys)
	}
	if status, ok := body["status"].(float64); !ok || status != float64(want) {
		return nil, fmt.Errorf("application status %v, want %d (msg %v)", body["status"], want, body["msg"])
	}
	if _, ok := body["msg"].(string); !ok {
		return nil, fmt.Errorf("envelope msg must be a string, got %s", brief(body["msg"]))
	}
	return body["data"], nil
}

func normalized(text string) any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		panic(err)
	}
	return value
}

// oneOf checks data equals one of the allowed values (canonical JSON).
func oneOf(what string, data any, allowed []string) error {
	for _, value := range allowed {
		if reflect.DeepEqual(data, normalized(value)) {
			return nil
		}
	}
	if len(allowed) == 1 {
		return fmt.Errorf("%s: got %s, want %s", what, brief(data), allowed[0])
	}
	return fmt.Errorf("%s: got %s, want one of %s", what, brief(data), strings.Join(allowed, " | "))
}

func brief(value any) string {
	text, ok := value.(string)
	if !ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		text = string(encoded)
	}
	if len(text) > 300 {
		return text[:300] + "..."
	}
	return text
}

var entityFields = map[string][]string{
	ttdata.Config:  {"description", "name", "value"},
	ttdata.Station: {"id", "name", "stayTime"},
	ttdata.Train:   {"averageSpeed", "confortClass", "economyClass", "id"},
	ttdata.Travel:  {"endTime", "routeId", "startingStationId", "startingTime", "stationsId", "terminalStationId", "trainTypeId", "tripId"},
	ttdata.Route:   {"distances", "id", "startStationId", "stations", "terminalStationId"},
	ttdata.Price:   {"basicPriceRate", "firstClassPriceRate", "id", "routeId", "trainType"},
}

var stringFields = map[string][]string{
	ttdata.Config:  {"name", "value", "description"},
	ttdata.Station: {"id", "name"},
	ttdata.Train:   {"id"},
	ttdata.Travel:  {"trainTypeId", "routeId", "startingStationId", "stationsId", "terminalStationId"},
	ttdata.Route:   {"id", "startStationId", "terminalStationId"},
	ttdata.Price:   {"id", "trainType", "routeId"},
}

// validateEntity checks an element of a service's list has the service's exact fields and
// their types (servicebench's validateEntityValue).
func validateEntity(service string, object map[string]any) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, entityFields[service]) {
		return fmt.Errorf("fields %v, want %v", keys, entityFields[service])
	}
	for _, field := range stringFields[service] {
		if _, ok := object[field].(string); !ok {
			return fmt.Errorf("%s must be a string, got %s", field, brief(object[field]))
		}
	}
	integer := func(field string) error {
		value, ok := object[field].(float64)
		if !ok || math.Trunc(value) != value {
			return fmt.Errorf("%s must be an integer, got %s", field, brief(object[field]))
		}
		return nil
	}
	switch service {
	case ttdata.Station:
		return integer("stayTime")
	case ttdata.Train:
		for _, field := range []string{"economyClass", "confortClass", "averageSpeed"} {
			if err := integer(field); err != nil {
				return err
			}
		}
	case ttdata.Route:
		stations, okStations := object["stations"].([]any)
		distances, okDistances := object["distances"].([]any)
		if !okStations || !okDistances || len(stations) != len(distances) {
			return fmt.Errorf("stations and distances must be arrays of one length")
		}
		for _, station := range stations {
			if _, ok := station.(string); !ok {
				return fmt.Errorf("stations must hold strings")
			}
		}
		for _, distance := range distances {
			if value, ok := distance.(float64); !ok || math.Trunc(value) != value {
				return fmt.Errorf("distances must hold integers")
			}
		}
	case ttdata.Price:
		for _, field := range []string{"basicPriceRate", "firstClassPriceRate"} {
			if value, ok := object[field].(float64); !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%s must be a number", field)
			}
		}
	case ttdata.Travel:
		if _, err := entityKey(service, object); err != nil {
			return err
		}
		for _, field := range []string{"startingTime", "endTime"} {
			if err := integer(field); err != nil {
				return err
			}
		}
	}
	return nil
}

// entityKey is an entity's key in its service: a config's name, a trip's id, else its id.
func entityKey(service string, object map[string]any) (string, error) {
	switch service {
	case ttdata.Config:
		name, ok := object["name"].(string)
		if !ok {
			return "", fmt.Errorf("name must be a string")
		}
		return name, nil
	case ttdata.Travel:
		trip, ok := object["tripId"].(map[string]any)
		if !ok || len(trip) != 2 {
			return "", fmt.Errorf("tripId must be an object of type and number, got %s", brief(object["tripId"]))
		}
		kind, okKind := trip["type"].(string)
		number, okNumber := trip["number"].(string)
		if !okKind || (kind != "G" && kind != "D") || !okNumber || number == "" {
			return "", fmt.Errorf("tripId must have type G or D and a non-empty number, got %s", brief(trip))
		}
		return kind + number, nil
	}
	id, ok := object["id"].(string)
	if !ok {
		return "", fmt.Errorf("id must be a string")
	}
	return id, nil
}
