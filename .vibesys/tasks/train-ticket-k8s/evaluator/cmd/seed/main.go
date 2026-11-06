// Command seed creates the train-ticket-k8s fixture through the application's public API (the
// benchmark's seed Job runs it, once the deployment serves): for each record of the dataset, a
// config, two stations, a train, the route between the stations, a price and a trip, each
// checked to be created, then every entity read back and compared. It prints a JSON summary
// as its last line and exits 1 on any unexpected response.
//
//	tt-seed --dataset FILE --target http://ts-gateway:8080
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"time"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

func main() {
	datasetPath := flag.String("dataset", "", "the dataset file bench.py's dataset() wrote")
	target := flag.String("target", "http://ts-gateway:8080", "the gateway's base URL")
	timeout := flag.Duration("timeout", 10*time.Minute, "how long the whole seed may take")
	flag.Parse()
	if *datasetPath == "" {
		fmt.Fprintln(os.Stderr, "seed: --dataset is required")
		os.Exit(2)
	}
	data, err := ttdata.Load(*datasetPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	s := &seeder{
		client: &http.Client{Timeout: 20 * time.Second},
		target: *target,
		auth:   "Bearer " + ttdata.AdminToken(data.Seed, 0x5eed),
	}
	started := time.Now()
	if err := s.seed(ctx, data); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	summary, _ := json.Marshal(map[string]any{
		"records": len(data.Records), "entities": 7 * len(data.Records), "requests": s.requests,
		"dataset_digest": data.Digest(), "seconds": time.Since(started).Seconds(),
	})
	fmt.Println(string(summary))
}

type seeder struct {
	client   *http.Client
	target   string
	auth     string
	requests int
}

type creation struct {
	path   string
	body   any
	status int // the HTTP status a create answers
	read   string
	want   any // what the read must return as data
}

func creations(record ttdata.Record) []creation {
	path := ttdata.Path
	return []creation{
		{ttdata.Collection(ttdata.Config), record.Config, http.StatusCreated,
			path(ttdata.Config, "/configs/"+url.PathEscape(record.Config.Name)), record.Config},
		{ttdata.Collection(ttdata.Station), record.StationA, http.StatusCreated,
			path(ttdata.Station, "/stations/name/"+record.StationA.ID), record.StationA.Name},
		{ttdata.Collection(ttdata.Station), record.StationB, http.StatusCreated,
			path(ttdata.Station, "/stations/name/"+record.StationB.ID), record.StationB.Name},
		{ttdata.Collection(ttdata.Train), record.Train, http.StatusOK,
			path(ttdata.Train, "/trains/"+record.Train.ID), record.Train},
		{ttdata.Collection(ttdata.Route), record.Route.Input(), http.StatusOK,
			path(ttdata.Route, "/routes/"+record.Route.ID), record.Route},
		{ttdata.Collection(ttdata.Price), record.Price, http.StatusCreated,
			path(ttdata.Price, "/prices/"+record.Price.RouteID+"/"+record.Price.TrainType), record.Price},
		{ttdata.Collection(ttdata.Travel), record.Trip.Input(), http.StatusCreated,
			path(ttdata.Travel, "/trips/"+record.Trip.TripID.String()), record.Trip},
	}
}

func (s *seeder) seed(ctx context.Context, data *ttdata.Dataset) error {
	for index, record := range data.Records {
		for _, c := range creations(record) {
			if _, err := s.call(ctx, http.MethodPost, c.path, c.body, c.status, 1); err != nil {
				return fmt.Errorf("record %d: create %s: %w", index, c.path, err)
			}
		}
	}
	for index, record := range data.Records {
		for _, c := range creations(record) {
			got, err := s.call(ctx, http.MethodGet, c.read, nil, http.StatusOK, 1)
			if err != nil {
				return fmt.Errorf("record %d: read back %s: %w", index, c.read, err)
			}
			want, _ := json.Marshal(c.want)
			var expected any
			_ = json.Unmarshal(want, &expected)
			if !reflect.DeepEqual(got, expected) {
				return fmt.Errorf("record %d: read back %s: got %v, want %s", index, c.read, got, want)
			}
		}
	}
	return nil
}

// call sends one request, retrying a transport error, and checks the envelope's status.
func (s *seeder) call(ctx context.Context, method, path string, body any, status, appStatus int) (any, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		request, err := http.NewRequestWithContext(ctx, method, s.target+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", s.auth)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		s.requests++
		response, err := s.client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode != status {
			return nil, fmt.Errorf("HTTP %d, want %d: %.300s", response.StatusCode, status, raw)
		}
		var envelope struct {
			Status *int `json:"status"`
			Data   any  `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Status == nil {
			return nil, fmt.Errorf("not the services' envelope: %.300s", raw)
		}
		if *envelope.Status != appStatus {
			return nil, fmt.Errorf("application status %d, want %d: %.300s", *envelope.Status, appStatus, raw)
		}
		return envelope.Data, nil
	}
	return nil, fmt.Errorf("after 5 attempts: %w", lastErr)
}
