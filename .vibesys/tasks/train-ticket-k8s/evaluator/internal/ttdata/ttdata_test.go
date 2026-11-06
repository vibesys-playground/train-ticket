package ttdata

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// datasetPy reads the constants dataset.py pins.
func datasetPy(t *testing.T) File {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "..", "dataset.py"))
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) string {
		match := regexp.MustCompile(`(?m)^` + name + ` = (.+?)(\s+#.*)?$`).FindSubmatch(text)
		if match == nil {
			t.Fatalf("dataset.py has no %s", name)
		}
		return strings.Trim(string(match[1]), `"`)
	}
	seed, err := strconv.ParseUint(find("SEED"), 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	records, err := strconv.Atoi(find("RECORDS"))
	if err != nil {
		t.Fatal(err)
	}
	return File{Seed: seed, Records: records, Digest: find("DIGEST")}
}

func TestDatasetPyPinsThisDataset(t *testing.T) {
	file := datasetPy(t)
	if got := Generate(file.Seed, file.Records).Digest(); got != file.Digest {
		t.Fatalf("seed %d, %d records: digest %s, dataset.py pins %s (a change of the fixture grammar changes the task: update DIGEST)", file.Seed, file.Records, got, file.Digest)
	}
	path := filepath.Join(t.TempDir(), "dataset.json")
	encoded, _ := json.Marshal(file)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	file.Digest = strings.Repeat("0", 64)
	encoded, _ = json.Marshal(file)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("a wrong digest was accepted: %v", err)
	}
}

func TestRecordsAreDeterministicConnectedAndUnique(t *testing.T) {
	file := datasetPy(t)
	first, second := Generate(file.Seed, file.Records), Generate(file.Seed, file.Records)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("one seed made two datasets")
	}
	if reflect.DeepEqual(first.Records, Generate(file.Seed+1, file.Records).Records) {
		t.Fatal("two seeds made one dataset")
	}
	seen := map[string]bool{}
	unique := func(kind, key string) {
		if seen[kind+"/"+key] {
			t.Fatalf("%s %q repeats", kind, key)
		}
		seen[kind+"/"+key] = true
	}
	tripID := regexp.MustCompile(`^[GD][0-9]{7}$`)
	for i, r := range first.Records {
		unique("config", r.Config.Name)
		unique("station", r.StationA.ID)
		unique("station", r.StationB.ID)
		unique("station-name", r.StationA.Name)
		unique("station-name", r.StationB.Name)
		unique("train", r.Train.ID)
		unique("route", r.Route.ID)
		unique("price", r.Price.ID)
		unique("trip", r.Trip.TripID.String())
		if r.Route.StartStationID != r.StationA.ID || r.Route.TerminalStationID != r.StationB.ID ||
			!reflect.DeepEqual(r.Route.Stations, []string{r.StationA.ID, r.StationB.ID}) {
			t.Fatalf("record %d: the route does not join its stations: %+v", i, r.Route)
		}
		if r.Price.RouteID != r.Route.ID || r.Price.TrainType != r.Train.ID || r.Trip.RouteID != r.Route.ID || r.Trip.TrainTypeID != r.Train.ID {
			t.Fatalf("record %d: price or trip does not reference its route and train", i)
		}
		if !tripID.MatchString(r.Trip.TripID.String()) || r.Trip.EndTime <= r.Trip.StartingTime {
			t.Fatalf("record %d: trip %+v", i, r.Trip)
		}
		// Route and price ids are long enough that the services keep them (shorter ones get a
		// fresh UUID on create).
		if len(r.Route.ID) < 10 || len(r.Price.ID) < 10 {
			t.Fatalf("record %d: ids %q %q too short", i, r.Route.ID, r.Price.ID)
		}
	}
}

func TestInputsCarryTheEntity(t *testing.T) {
	r := Generate(1, 2).Records[0]
	in := r.Route.Input()
	if in.ID != r.Route.ID || in.StationList != r.StationA.ID+","+r.StationB.ID || in.DistanceList != "0,"+strconv.Itoa(r.Route.Distances[1]) {
		t.Fatalf("route input %+v of %+v", in, r.Route)
	}
	trip := r.Trip.Input()
	if trip.TripID != r.Trip.TripID.Type+r.Trip.TripID.Number || trip.EndTime != r.Trip.EndTime {
		t.Fatalf("trip input %+v of %+v", trip, r.Trip)
	}
}

func TestAdminTokenIsAValidAdminJWT(t *testing.T) {
	token := AdminToken(1, 2)
	if token != AdminToken(1, 2) || token == AdminToken(1, 3) {
		t.Fatal("the token is not a function of its identity")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("%d parts", len(parts))
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("signature is not HS256 with the services' key")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Sub   string   `json:"sub"`
		ID    string   `json:"id"`
		Roles []string `json:"roles"`
		Exp   int64    `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Sub) != 24 || claims.ID != claims.Sub || !reflect.DeepEqual(claims.Roles, []string{"ROLE_ADMIN"}) || claims.Exp != TokenExpiresAt {
		t.Fatalf("claims %+v", claims)
	}
}

func TestPathsAreTheServicesRoutes(t *testing.T) {
	cases := map[string]string{
		Collection(Config):         "/api/v1/configservice/configs",
		Collection(Station):        "/api/v1/stationservice/stations",
		Collection(Train):          "/api/v1/trainservice/trains",
		Collection(Travel):         "/api/v1/travelservice/trips",
		Collection(Route):          "/api/v1/routeservice/routes",
		Collection(Price):          "/api/v1/priceservice/prices",
		Path(Price, "/prices/a/b"): "/api/v1/priceservice/prices/a/b",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}
