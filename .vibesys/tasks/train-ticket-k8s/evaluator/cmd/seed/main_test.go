package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// fakeAPI answers creates with each service's status and reads with what was created (or a
// value the test planted), as the six services' envelopes.
type fakeAPI struct {
	mu      sync.Mutex
	data    *ttdata.Dataset
	created map[string]int
	reads   map[string]any
	auth    map[string]bool
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth[r.Header.Get("Authorization")] = true
	write := func(status, app int, data any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": app, "msg": "m", "data": data})
	}
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		if !json.Valid(body) || r.Header.Get("Content-Type") != "application/json" {
			write(http.StatusBadRequest, 0, nil)
			return
		}
		f.created[r.URL.EscapedPath()]++
		status := http.StatusCreated
		if strings.HasSuffix(r.URL.Path, "/trains") || strings.HasSuffix(r.URL.Path, "/routes") {
			status = http.StatusOK
		}
		write(status, 1, nil)
		return
	}
	value, ok := f.reads[r.URL.EscapedPath()]
	if !ok {
		write(http.StatusOK, 0, nil)
		return
	}
	write(http.StatusOK, 1, value)
}

func newFake(data *ttdata.Dataset) *fakeAPI {
	f := &fakeAPI{data: data, created: map[string]int{}, reads: map[string]any{}, auth: map[string]bool{}}
	for _, record := range data.Records {
		for _, c := range creations(record) {
			f.reads[c.read] = c.want
		}
	}
	return f
}

func TestSeedCreatesEveryEntityAndReadsItBack(t *testing.T) {
	data := ttdata.Generate(3, 4)
	fake := newFake(data)
	server := httptest.NewServer(fake)
	defer server.Close()
	s := &seeder{client: server.Client(), target: server.URL, auth: "Bearer " + ttdata.AdminToken(data.Seed, 0x5eed)}
	if err := s.seed(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range fake.created {
		total += n
	}
	if total != 7*len(data.Records) || fake.created[ttdata.Collection(ttdata.Station)] != 2*len(data.Records) {
		t.Fatalf("created %v", fake.created)
	}
	if len(fake.auth) != 1 || !fake.auth[s.auth] {
		t.Fatalf("authorization headers %v", fake.auth)
	}
	if s.requests != 14*len(data.Records) {
		t.Fatalf("%d requests", s.requests)
	}
}

func TestSeedFailsOnAWrongReadBack(t *testing.T) {
	data := ttdata.Generate(3, 4)
	fake := newFake(data)
	wrong := data.Records[2].Train
	wrong.AverageSpeed++
	fake.reads[creations(data.Records[2])[3].read] = wrong
	server := httptest.NewServer(fake)
	defer server.Close()
	s := &seeder{client: server.Client(), target: server.URL, auth: "Bearer x"}
	err := s.seed(context.Background(), data)
	if err == nil || !strings.Contains(err.Error(), "record 2: read back /api/v1/trainservice/trains/") {
		t.Fatalf("a wrong read-back passed: %v", err)
	}
}
