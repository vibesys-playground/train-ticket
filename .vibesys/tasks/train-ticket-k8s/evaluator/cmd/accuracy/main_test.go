package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/gen/vibesys/evaluator/v1"

	"vibesys/train-ticket-k8s-evaluator/internal/ttaccuracy"
	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// mainEnv makes the test binary run the generator's main (as loadgen runs it).
const mainEnv = "TT_ACCURACY_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// datasetFile writes the dataset file of dataset.py's constants, as bench.py's dataset() does.
func datasetFile(t *testing.T) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "..", "dataset.py"))
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) string {
		match := regexp.MustCompile(`(?m)^` + name + ` = (.+)$`).FindSubmatch(text)
		if match == nil {
			t.Fatalf("dataset.py has no %s", name)
		}
		return strings.Trim(string(match[1]), `"`)
	}
	seed, _ := strconv.ParseUint(find("SEED"), 0, 64)
	records, _ := strconv.Atoi(find("RECORDS"))
	encoded, _ := json.Marshal(ttdata.File{Seed: seed, Records: records, Digest: find("DIGEST")})
	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func frame(in *bufio.Reader) ([]byte, error) {
	length, err := binary.ReadUvarint(in)
	if err != nil {
		return nil, err
	}
	out := make([]byte, length)
	_, err = io.ReadFull(in, out)
	return out, err
}

// TestTheGeneratorRunsUnderCapture runs `generate` as an accuracy run's phase does (open loop,
// capture on): the SDK accepts the workload (operation names included), and every operation is
// one step that keeps its bodies, named after the oracle's operation.
func TestTheGeneratorRunsUnderCapture(t *testing.T) {
	const n = 2000
	input := &pb.PhaseInput{SampleSeed: 7, PhaseIndex: 2, Model: pb.LoadModel_LOAD_MODEL_OPEN, Rate: 40, Secs: 60, Capture: true}
	encoded, err := proto.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.binpb")
	if err := os.WriteFile(inputPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(self, "generate", "--phase-input", inputPath, "--dataset", datasetFile(t), "--state", t.TempDir())
	command.Env = append(os.Environ(), mainEnv+"=1")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReaderSize(stdout, 1<<16)
	var frames [][]byte
	for len(frames) < n+2 { // the FileHeader, the RequestsHeader, the operations
		f, err := frame(in)
		if err != nil {
			t.Fatalf("frame %d: %v; stderr: %s", len(frames), err, stderr.String())
		}
		frames = append(frames, f)
	}
	_ = stdout.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("generate: %v; stderr: %s", err, stderr.String())
	}
	header := &pb.RequestsHeader{}
	if err := proto.Unmarshal(frames[1], header); err != nil {
		t.Fatal(err)
	}
	names := header.GetOperationNames()
	want := ttaccuracy.Workload().Operations
	if len(names) != len(want) {
		t.Fatalf("%d operation names, want %d", len(names), len(want))
	}
	for i, name := range names {
		if name != want[i].Name {
			t.Fatalf("operation %d is %q, want %q", i, name, want[i].Name)
		}
	}
	bodies := 0
	for i, f := range frames[2:] {
		op := &pb.PlannedOperation{}
		if err := proto.Unmarshal(f, op); err != nil {
			t.Fatal(err)
		}
		name := names[op.GetOperation()]
		if !op.GetKeepBodies() || len(op.GetSteps()) != 1 {
			t.Fatalf("operation %d (%s): keep bodies %v, %d steps", i, name, op.GetKeepBodies(), len(op.GetSteps()))
		}
		if body := op.GetSteps()[0].GetHttp().GetRaw(); body != nil { // a JSON body without captures
			bodies++
			if err := javaTyped(string(body)); err != nil {
				t.Fatalf("operation %d (%s): %v: %s", i, name, err, body)
			}
		}
	}
	if bodies < n/3 {
		t.Fatalf("%d bodies in %d operations", bodies, n)
	}
}

// integers are the entities' fields the services read as an int, a long or a Date: Jackson
// refuses a number with a fraction or an exponent for them (1600000000000.0, say).
var integers = map[string]bool{"stayTime": true, "economyClass": true, "confortClass": true, "averageSpeed": true,
	"startingTime": true, "endTime": true}

// javaTyped: the body is JSON (an object, or the route ids of a trips-by-route query) whose
// integer fields are integer literals, and a route's distance list is integers.
func javaTyped(body string) error {
	d := json.NewDecoder(strings.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for k, v := range m {
		if n, ok := v.(json.Number); ok && integers[k] && strings.ContainsAny(n.String(), ".eE") {
			return fmt.Errorf("%s is %s, not an integer", k, n)
		}
	}
	if list, ok := m["distanceList"].(string); ok {
		for _, d := range strings.Split(list, ",") {
			if _, err := strconv.Atoi(d); err != nil {
				return fmt.Errorf("distanceList %q", list)
			}
		}
	}
	return nil
}
