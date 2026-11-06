package ttoracle

import (
	"encoding/json"
	"fmt"

	"vibesys/train-ticket-k8s-evaluator/internal/ttcatalog"
	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// ForFixture is the oracle's dataset for a run of the task: when the history starts the services
// hold their startup catalog (ttcatalog) and the fixture the seed Job created through the API
// (ttdata); a restarted service re-applies its part of the catalog only.
func ForFixture(data *ttdata.Dataset) (*Dataset, error) {
	d := &Dataset{Initial: map[string][]map[string]any{}, Reinit: map[string][]map[string]any{}}
	for table, entities := range ttcatalog.Entities() {
		for _, raw := range entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("catalog %s: %w", table, err)
			}
			d.Initial[table] = append(d.Initial[table], m)
			d.Reinit[table] = append(d.Reinit[table], m)
		}
	}
	type entity struct {
		table string
		value any
	}
	for _, r := range data.Records {
		for _, e := range []entity{
			{ttdata.Config, r.Config}, {ttdata.Station, r.StationA}, {ttdata.Station, r.StationB},
			{ttdata.Train, r.Train}, {ttdata.Route, r.Route}, {ttdata.Price, r.Price}, {ttdata.Travel, r.Trip},
		} {
			encoded, err := json.Marshal(e.value)
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal(encoded, &m); err != nil {
				return nil, err
			}
			d.Initial[e.table] = append(d.Initial[e.table], m)
		}
	}
	return d, nil
}
