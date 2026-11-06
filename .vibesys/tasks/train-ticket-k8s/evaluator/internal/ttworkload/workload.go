// Package ttworkload is the train-ticket-k8s benchmark's workload on the vibesys-evaluator Go
// SDK: the operations of the mix with their weights, how each request is drawn and how an
// update changes the model the later requests build on. cmd/generator runs it as the generator
// that loadgen reads the request stream from (bench.py names it).
//
// It ports the servicebench Train Ticket benchmark adapter (vibesys
// resources/evaluators/microservice/apps/trainticket: application.go, model.go,
// operations.go). The benchmark only measures: whether the responses are right is the accuracy
// check's to judge, from a captured history (each request's Info says what it was built from).
//
// Operations, each on a fixture record drawn uniformly (weights out of 95):
//
//   - list_<service> (35): the collection;
//   - read_<service> (25): the drawn entity by its key (a station's name by its id, a price
//     by its route and train);
//   - update_read_<service> (35): a full update of the drawn entity, then a read of the
//     written value (read your write), then, where the update moved a secondary key (a
//     station's name, a route's direction, a price's route), a read by the retired key.
//
// Each phase's model starts from the fixture (the generator never learns what earlier phases
// wrote), so after the first phase a read by a secondary key an update moved may find
// nothing, which the services answer with status 0 inside HTTP 200; a benchmark counts it as a
// completed operation. The servicebench mix also had create_read_delete_config (weight 5),
// which workload.HTTP("DELETE", ...) can now send; it is not in the mix yet.
package ttworkload

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// Key names one entity of the fixture: a service's entity of record Item.
type Key struct {
	Service string
	Item    int
}

// Model is the fixture of a sample and the phase's updates of it: per entity, its latest
// value as canonical JSON (the fixture's, then each update's as it is built).
type Model struct {
	data    *ttdata.Dataset
	auth    string
	current map[Key]string
}

// NewModel is the model of a sample: rand is the sample's model stream, which names the
// sample's admin identity.
func NewModel(data *ttdata.Dataset, rand *workload.Rand) (*Model, error) {
	token := rand.Child("token")
	m := &Model{
		data:    data,
		auth:    "Bearer " + ttdata.AdminToken(token.Uint64(), token.Uint64()),
		current: map[Key]string{},
	}
	for item, record := range data.Records {
		for _, service := range ttdata.Services {
			m.current[Key{service, item}] = canonical(entityOf(service, record))
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

// Latest is the entity's value the next request builds on: the last update built, or the
// fixture's.
func (m *Model) Latest(key Key) string { return m.current[key] }

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
		ops = append(ops, workload.Operation[*Model]{Name: "list_" + service, Weight: listWeights[service], Build: buildList(service)})
	}
	for _, service := range ttdata.Services {
		ops = append(ops, workload.Operation[*Model]{Name: "read_" + service, Weight: readWeights[service], Build: buildRead(service)})
	}
	for _, service := range ttdata.Services {
		ops = append(ops, workload.Operation[*Model]{Name: "update_read_" + service, Weight: updateWeights[service], Build: buildUpdate(service)})
	}
	return workload.Workload[*ttdata.Dataset, *Model]{Dataset: ttdata.Load, Model: NewModel, Operations: ops}
}

// ---------------------------------------------------------------- requests

// ItemInfo is a list's Info: the record drawn (the accuracy check looks its entity up).
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
		m.current[key] = value
		steps := []workload.Step{update.Write, m.get(ReadPath(service, value))}
		if update.Lookup != "" {
			steps = append(steps, m.get(update.Lookup))
		}
		return workload.Request{
			Steps: steps,
			Info:  UpdateInfo{Item: item, Value: json.RawMessage(value), Retired: update.Retired},
		}
	}
}
