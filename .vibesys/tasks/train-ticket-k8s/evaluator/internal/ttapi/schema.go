// Package ttapi: the Train Ticket API as the accuracy oracle sees it: strict entity schemas,
// the request each operation maps to, and the parser of every response.
package ttapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type fieldKind int

const (
	fStr fieldKind = iota // string
	fNE                   // non-empty string
	fInt
	fNum
	fStrList
	fIntList
	fTripID
)

var schema = map[string]map[string]fieldKind{
	"config":  {"name": fNE, "value": fStr, "description": fStr},
	"station": {"id": fNE, "name": fNE, "stayTime": fInt},
	"train":   {"id": fNE, "economyClass": fInt, "confortClass": fInt, "averageSpeed": fInt},
	"route":   {"id": fNE, "stations": fStrList, "distances": fIntList, "startStationId": fNE, "terminalStationId": fNE},
	"price":   {"id": fNE, "trainType": fNE, "routeId": fNE, "basicPriceRate": fNum, "firstClassPriceRate": fNum},
	"travel": {"tripId": fTripID, "trainTypeId": fNE, "routeId": fNE, "startingTime": fInt, "startingStationId": fNE,
		"stationsId": fNE, "terminalStationId": fNE, "endTime": fInt},
}

func isInt(v any) bool {
	f, ok := v.(float64)
	return ok && f == float64(int64(f))
}

// Canon strictly validates an entity (exact field set, exact types) and returns its key
// ("table/id") and canonical JSON (sorted keys, numbers normalised). It is the
// "strict entity schemas" check of the existing suite.
func Canon(table string, raw []byte) (key, canon string, err error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", "", fmt.Errorf("%s: not an object: %v", table, err)
	}
	sc := schema[table]
	for f := range m {
		if _, ok := sc[f]; !ok {
			return "", "", fmt.Errorf("%s: unexpected field %q", table, f)
		}
	}
	for f, k := range sc {
		v, ok := m[f]
		if !ok {
			return "", "", fmt.Errorf("%s: missing field %q", table, f)
		}
		bad := false
		switch k {
		case fStr:
			_, bad = v.(string)
			bad = !bad
		case fNE:
			s, ok := v.(string)
			bad = !ok || s == ""
		case fInt:
			bad = !isInt(v)
		case fNum:
			_, ok := v.(float64)
			bad = !ok
		case fStrList, fIntList:
			l, ok := v.([]any)
			bad = !ok
			for _, e := range l {
				if k == fStrList {
					if _, ok := e.(string); !ok {
						bad = true
					}
				} else if !isInt(e) {
					bad = true
				}
			}
		case fTripID:
			t, ok := v.(map[string]any)
			if !ok || len(t) != 2 {
				bad = true
				break
			}
			ty, _ := t["type"].(string)
			nu, _ := t["number"].(string)
			bad = (ty != "G" && ty != "D") || nu == ""
		}
		if bad {
			return "", "", fmt.Errorf("%s: field %q has bad value %v", table, f, v)
		}
	}
	if table == "route" && len(m["stations"].([]any)) != len(m["distances"].([]any)) {
		return "", "", fmt.Errorf("route: stations/distances length differ")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(m)
	canon = strings.TrimSpace(buf.String())
	return table + "/" + keyOfMap(table, m), canon, nil
}

func keyOfMap(table string, m map[string]any) string {
	switch table {
	case "config":
		return m["name"].(string)
	case "travel":
		t := m["tripId"].(map[string]any)
		return t["type"].(string) + t["number"].(string)
	}
	return m["id"].(string)
}

var fieldCache sync.Map // canon -> map[string]any

func fields(canon string) map[string]any {
	if v, ok := fieldCache.Load(canon); ok {
		return v.(map[string]any)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(canon), &m)
	fieldCache.Store(canon, m)
	return m
}

func StrField(canon, f string) string { s, _ := fields(canon)[f].(string); return s }

func StrList(canon, f string) []string {
	var r []string
	l, _ := fields(canon)[f].([]any)
	for _, e := range l {
		r = append(r, e.(string))
	}
	return r
}

// KeyOf returns "table/id" of a canonical entity.
func KeyOf(table, canon string) string { return table + "/" + keyOfMap(table, fields(canon)) }

// WithID replaces the id of a route/price entity (server-generated id).
func WithID(canon, id string) string {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(canon), &m)
	m["id"] = id
	b, _ := json.Marshal(m)
	return string(b)
}

// ProjRoute: the travel service's own Route class (returned by GET /travelservice/routes/{tripId})
// has null startStationId/terminalStationId, so a join result is compared on these fields only.
func ProjRoute(canon string) string {
	m := fields(canon)
	b, _ := json.Marshal(map[string]any{"id": m["id"], "stations": m["stations"], "distances": m["distances"]})
	return string(b)
}

// CanonJoinRoute validates the join's route payload (id, stations, distances; other fields ignored).
func CanonJoinRoute(raw []byte) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	id, ok := m["id"].(string)
	st, ok2 := m["stations"].([]any)
	ds, ok3 := m["distances"].([]any)
	if !ok || id == "" || !ok2 || !ok3 || len(st) != len(ds) {
		return "", fmt.Errorf("join route: bad id/stations/distances")
	}
	for _, e := range st {
		if _, ok := e.(string); !ok {
			return "", fmt.Errorf("join route: bad station")
		}
	}
	for _, e := range ds {
		if !isInt(e) {
			return "", fmt.Errorf("join route: bad distance")
		}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "stations": st, "distances": ds})
	return string(b), nil
}
