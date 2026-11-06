// Package ttcatalog is Train Ticket's startup catalog: the entities each of the six services
// writes when it starts (its InitData: a config, 13 stations, 6 train types, 10 routes, 10 prices
// and 5 trips), as the services answer them. The seed Job leaves them as they are and adds the
// fixture (internal/ttdata); a restarted service writes them again, which the accuracy oracle
// models (create-if-absent for config, station, train and trip; an overwrite for route and
// price).
//
// startup.json was read from the base application on kind (the oracle spike's baseline) and
// matches the services' InitData.java; a candidate that changes the catalog changes what the
// application answers.
package ttcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed startup.json
var startup []byte

// Entities is the catalog: table -> its entities, as the services return them.
func Entities() map[string][]json.RawMessage {
	var tables map[string][]json.RawMessage
	if err := json.Unmarshal(startup, &tables); err != nil {
		panic(fmt.Sprintf("ttcatalog: startup.json: %v", err))
	}
	return tables
}

// IDs is the key of every catalog entity of table (a config's name, a trip's "G1234", else its
// id), in startup.json's order.
func IDs(table string) []string {
	var ids []string
	for _, raw := range Entities()[table] {
		var e struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			TripID struct {
				Type   string `json:"type"`
				Number string `json:"number"`
			} `json:"tripId"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			panic(fmt.Sprintf("ttcatalog: %s: %v", table, err))
		}
		switch table {
		case "config":
			ids = append(ids, e.Name)
		case "travel":
			ids = append(ids, e.TripID.Type+e.TripID.Number)
		default:
			ids = append(ids, e.ID)
		}
	}
	return ids
}
