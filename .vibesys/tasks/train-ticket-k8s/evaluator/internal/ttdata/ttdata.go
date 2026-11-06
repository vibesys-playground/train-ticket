// Package ttdata is the train-ticket-k8s dataset: the fixture records the seed Job creates
// through the public API before the measured phases, the entity shapes the six services
// return, their API paths, and the admin token every request carries.
//
// It is a port of the servicebench Train Ticket adapter's fixture grammar
// (resources/evaluators/microservice/appsupport/trainticket/fuzz.go and
// apps/trainticket/model.go in vibesys) onto the vibesys-evaluator Rand, so the dataset is a
// pure function of its seed and record count, the same on every platform. The dataset file
// bench.py writes holds only those two numbers and the digest of the records they make
// (dataset.py); every program regenerates the records and checks the digest.
package ttdata

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/workload"
)

// The six services of the task, in the order the fixture creates their entities' owners.
const (
	Config  = "config"
	Station = "station"
	Train   = "train"
	Travel  = "travel"
	Route   = "route"
	Price   = "price"
)

// Services lists the services the workload exercises.
var Services = []string{Config, Station, Train, Travel, Route, Price}

var prefixes = map[string]string{
	Config:  "/api/v1/configservice",
	Station: "/api/v1/stationservice",
	Train:   "/api/v1/trainservice",
	Travel:  "/api/v1/travelservice",
	Route:   "/api/v1/routeservice",
	Price:   "/api/v1/priceservice",
}

var collections = map[string]string{
	Config: "/configs", Station: "/stations", Train: "/trains",
	Travel: "/trips", Route: "/routes", Price: "/prices",
}

// Path is the API path of suffix below service's prefix.
func Path(service, suffix string) string {
	prefix, ok := prefixes[service]
	if !ok || !strings.HasPrefix(suffix, "/") {
		panic(fmt.Sprintf("ttdata.Path(%q, %q)", service, suffix))
	}
	return prefix + suffix
}

// Collection is the path of service's collection (list, create, update).
func Collection(service string) string { return Path(service, collections[service]) }

// ConfigEntity is a config as ts-config-service returns it.
type ConfigEntity struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// StationEntity is a station as ts-station-service returns it.
type StationEntity struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	StayTime int    `json:"stayTime"`
}

// TrainEntity is a train type as ts-train-service returns it.
type TrainEntity struct {
	ID           string `json:"id"`
	EconomyClass int    `json:"economyClass"`
	ConfortClass int    `json:"confortClass"`
	AverageSpeed int    `json:"averageSpeed"`
}

// RouteEntity is a route as ts-route-service returns it.
type RouteEntity struct {
	ID                string   `json:"id"`
	Stations          []string `json:"stations"`
	Distances         []int    `json:"distances"`
	StartStationID    string   `json:"startStationId"`
	TerminalStationID string   `json:"terminalStationId"`
}

// RouteInput is the body ts-route-service creates and modifies a route from.
type RouteInput struct {
	ID           string `json:"id"`
	StartStation string `json:"startStation"`
	EndStation   string `json:"endStation"`
	StationList  string `json:"stationList"`
	DistanceList string `json:"distanceList"`
}

// Input is the route's create-or-modify body.
func (r RouteEntity) Input() RouteInput {
	distances := make([]string, len(r.Distances))
	for i, d := range r.Distances {
		distances[i] = fmt.Sprint(d)
	}
	return RouteInput{
		ID: r.ID, StartStation: r.StartStationID, EndStation: r.TerminalStationID,
		StationList: strings.Join(r.Stations, ","), DistanceList: strings.Join(distances, ","),
	}
}

// PriceEntity is a price config as ts-price-service returns it.
type PriceEntity struct {
	ID                  string  `json:"id"`
	TrainType           string  `json:"trainType"`
	RouteID             string  `json:"routeId"`
	BasicPriceRate      float64 `json:"basicPriceRate"`
	FirstClassPriceRate float64 `json:"firstClassPriceRate"`
}

// TripID is a trip's identity as ts-travel-service returns it.
type TripID struct {
	Type   string `json:"type"`
	Number string `json:"number"`
}

// String is the trip id of paths and inputs ("G1234567").
func (t TripID) String() string { return t.Type + t.Number }

// TripEntity is a trip as ts-travel-service returns it (times in epoch milliseconds).
type TripEntity struct {
	TripID            TripID `json:"tripId"`
	TrainTypeID       string `json:"trainTypeId"`
	RouteID           string `json:"routeId"`
	StartingTime      int64  `json:"startingTime"`
	StartingStationID string `json:"startingStationId"`
	StationsID        string `json:"stationsId"`
	TerminalStationID string `json:"terminalStationId"`
	EndTime           int64  `json:"endTime"`
}

// TripInput is the body ts-travel-service creates and updates a trip from.
type TripInput struct {
	TripID            string `json:"tripId"`
	TrainTypeID       string `json:"trainTypeId"`
	RouteID           string `json:"routeId"`
	StartingTime      int64  `json:"startingTime"`
	StartingStationID string `json:"startingStationId"`
	StationsID        string `json:"stationsId"`
	TerminalStationID string `json:"terminalStationId"`
	EndTime           int64  `json:"endTime"`
}

// Input is the trip's create-or-update body.
func (t TripEntity) Input() TripInput {
	return TripInput{
		TripID: t.TripID.String(), TrainTypeID: t.TrainTypeID, RouteID: t.RouteID,
		StartingTime: t.StartingTime, StartingStationID: t.StartingStationID,
		StationsID: t.StationsID, TerminalStationID: t.TerminalStationID, EndTime: t.EndTime,
	}
}

// Record is one connected fixture: a config, two stations, a train, the route between the
// stations, a price of the train on the route and a trip of the train on the route.
type Record struct {
	Config   ConfigEntity  `json:"config"`
	StationA StationEntity `json:"stationA"`
	StationB StationEntity `json:"stationB"`
	Train    TrainEntity   `json:"train"`
	Route    RouteEntity   `json:"route"`
	Price    PriceEntity   `json:"price"`
	Trip     TripEntity    `json:"trip"`
}

// Dataset is the fixture of a sample: the records the seed Job creates.
type Dataset struct {
	Seed    uint64   `json:"seed"`
	Records []Record `json:"records"`
}

// Generate makes count records from seed.
func Generate(seed uint64, count int) *Dataset {
	if count < 2 {
		panic(fmt.Sprintf("ttdata.Generate: %d records, want at least 2", count))
	}
	records := make([]Record, count)
	for index := range records {
		r := workload.NewRand(seed, "record", index)
		token := fmt.Sprintf("vs%03x%08x", index, uint32(r.Uint64()))
		stationA := StationEntity{ID: token + "a", Name: stationName(r, false, token), StayTime: 1 + r.Below(40)}
		stationB := StationEntity{ID: token + "b", Name: stationName(r, true, token), StayTime: 1 + r.Below(40)}
		train := TrainEntity{ID: "T" + token, EconomyClass: 100 + r.Below(800), ConfortClass: 50 + r.Below(250), AverageSpeed: 80 + r.Below(270)}
		route := RouteEntity{
			ID: uuid(r), Stations: []string{stationA.ID, stationB.ID}, Distances: []int{0, 100 + r.Below(1700)},
			StartStationID: stationA.ID, TerminalStationID: stationB.ID,
		}
		price := PriceEntity{
			ID: uuid(r), TrainType: train.ID, RouteID: route.ID,
			BasicPriceRate: Round4(0.1 + r.Random()*0.8), FirstClassPriceRate: Round4(0.9 + r.Random()),
		}
		kind := "G"
		if r.Below(2) == 0 {
			kind = "D"
		}
		start := int64(1_600_000_000_000) + int64(r.Uint64()%300_000_000_001)
		trip := TripEntity{
			TripID:      TripID{Type: kind, Number: fmt.Sprintf("%07d", 1_000_000+r.Below(9_000_000))},
			TrainTypeID: train.ID, RouteID: route.ID, StartingTime: start,
			StartingStationID: stationA.ID, StationsID: stationB.ID, TerminalStationID: stationB.ID,
			EndTime: start + int64(3_600_000+r.Below(39_600_001)),
		}
		config := ConfigEntity{
			Name:        configName(r, token),
			Value:       fmt.Sprintf("v-%016x", r.Uint64()),
			Description: fmt.Sprintf("d-%016x", r.Uint64()),
		}
		records[index] = Record{Config: config, StationA: stationA, StationB: stationB, Train: train, Route: route, Price: price, Trip: trip}
	}
	return &Dataset{Seed: seed, Records: records}
}

// Digest is the SHA-256 of the records' JSON, hex: what dataset.py pins.
func (d *Dataset) Digest() string {
	encoded, err := json.Marshal(d.Records)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// File is the dataset file bench.py's dataset() writes.
type File struct {
	Seed    uint64 `json:"seed"`
	Records int    `json:"records"`
	Digest  string `json:"digest"`
}

// Load regenerates the dataset a dataset file names and checks its digest.
func Load(path string) (*Dataset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if file.Records < 2 {
		return nil, fmt.Errorf("%s: records %d, want at least 2", path, file.Records)
	}
	data := Generate(file.Seed, file.Records)
	if got := data.Digest(); got != file.Digest {
		return nil, fmt.Errorf("the dataset of seed %d and %d records has digest %s, the dataset file says %s (dataset.py and internal/ttdata disagree)", file.Seed, file.Records, got, file.Digest)
	}
	return data, nil
}

func stationName(r *workload.Rand, terminal bool, token string) string {
	prefixes := []string{"Station A ", "North Hub ", "北站 "}
	if terminal {
		prefixes = []string{"Station B ", "South Hub ", "南站 "}
	}
	return prefixes[r.Below(len(prefixes))] + token
}

func configName(r *workload.Rand, token string) string {
	names := []string{token + "Config", "config " + token, "配置-" + token}
	return names[r.Below(len(names))]
}

func uuid(r *workload.Rand) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[0:8], r.Uint64())
	binary.BigEndian.PutUint64(raw[8:16], r.Uint64())
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(raw[0:4]), binary.BigEndian.Uint16(raw[4:6]),
		binary.BigEndian.Uint16(raw[6:8]), binary.BigEndian.Uint16(raw[8:10]), raw[10:16])
}

// Round4 rounds to four decimals, as the price rates are written.
func Round4(value float64) float64 { return math.Round(value*10_000) / 10_000 }

// The updates of the workload: each a function of the entity's current value and the
// operation's version word (servicebench's Updated* grammar).

// UpdatedStationName is a renamed station's name.
func UpdatedStationName(version uint64) string {
	prefixes := []string{"Renamed A ", "Transfer Hub A ", "换乘站甲 "}
	return fmt.Sprintf("%s%016x", prefixes[version%uint64(len(prefixes))], version)
}

// UpdatedStationStayTime is a renamed station's stay time.
func UpdatedStationStayTime(version uint64) int { return 41 + int(version%50) }

// UpdatedTrainSpeed is an updated train's average speed.
func UpdatedTrainSpeed(version uint64) int { return 351 + int(version%150) }

// UpdatedTrainEconomy is an updated train's economy class seats.
func UpdatedTrainEconomy(current int) int { return current + 7 }

// UpdatedRouteDistance is an updated route's distance.
func UpdatedRouteDistance(current int, version uint64) int { return current + 1 + int(version%99) }

// UpdatedPriceRates are an updated price's basic and first-class rates.
func UpdatedPriceRates(version uint64) (float64, float64) {
	return Round4(0.11 + float64(version%7800)/10_000), Round4(0.91 + float64((version>>16)%9800)/10_000)
}

// UpdatedTripEnd is an updated trip's end time.
func UpdatedTripEnd(current int64, version uint64) int64 {
	return current + int64(60_000+version%3_540_001)
}

// Token issue and expiry times: fixed, so the request stream is a function of the sample
// seed alone (the services check the signature and that the token has not expired).
const (
	TokenIssuedAt  = 1_700_000_000 // 2023-11-14
	TokenExpiresAt = 4_102_444_800 // 2100-01-01
)

// AdminToken is an HS256 JWT with the admin role that every Train Ticket service accepts
// (ts-common's JWTUtil: the key "secret"), for the identity drawn from identity.
func AdminToken(identity uint64, salt uint64) string {
	var raw [12]byte
	binary.BigEndian.PutUint64(raw[0:8], identity)
	binary.BigEndian.PutUint32(raw[8:12], uint32(salt))
	id := hex.EncodeToString(raw[:])
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"sub": id, "roles": []string{"ROLE_ADMIN"}, "id": id,
		"iat": TokenIssuedAt, "exp": TokenExpiresAt,
	})
	if err != nil {
		panic(err)
	}
	input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	signer := hmac.New(sha256.New, []byte("secret"))
	_, _ = signer.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(signer.Sum(nil))
}
