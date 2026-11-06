package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
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

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
)

// mainEnv makes the test binary run the generator's main (as loadgen runs it).
const mainEnv = "TT_GENERATOR_MAIN"

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

// generate runs `generate` as loadgen does and reads the RequestsHeader and n operations.
func generate(t *testing.T, input *pb.PhaseInput, dataset string, n int) (*pb.RequestsHeader, []*pb.PlannedOperation, [][]byte) {
	t.Helper()
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
	command := exec.Command(self, "generate", "--phase-input", inputPath, "--dataset", dataset, "--state", t.TempDir())
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
	_ = stdout.Close() // the end of the phase, as loadgen ends it
	if err := command.Wait(); err != nil {
		t.Fatalf("generate: %v; stderr: %s", err, stderr.String())
	}
	header := &pb.RequestsHeader{}
	if err := proto.Unmarshal(frames[1], header); err != nil {
		t.Fatal(err)
	}
	ops := make([]*pb.PlannedOperation, 0, n)
	for _, f := range frames[2:] {
		op := &pb.PlannedOperation{}
		if err := proto.Unmarshal(f, op); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	return header, ops, frames[1:]
}

func path(step *pb.Step) string {
	var text strings.Builder
	for _, part := range step.GetHttp().GetPath().GetParts() {
		text.WriteString(part.GetLiteral())
	}
	return text.String()
}

// TestTheStreamIsTheWorkload: the generator writes, for one phase, the same stream every time
// (and another for another sample), in the mix's proportions, every request authorized; a
// benchmark's phase keeps no bodies, and one under capture keeps every operation's.
func TestTheStreamIsTheWorkload(t *testing.T) {
	const n = 20000
	dataset := datasetFile(t)
	input := &pb.PhaseInput{SampleSeed: 7, PhaseIndex: 1, Model: pb.LoadModel_LOAD_MODEL_CLOSED, Clients: 8, Secs: 10}
	header, ops, frames := generate(t, input, dataset, n)
	_, _, again := generate(t, input, dataset, n)
	for i := range frames {
		if !bytes.Equal(frames[i], again[i]) {
			t.Fatalf("frame %d differs between two runs of one phase", i)
		}
	}
	other := proto.Clone(input).(*pb.PhaseInput)
	other.SampleSeed = 8
	if _, _, third := generate(t, other, dataset, 100); bytes.Equal(third[50], frames[50]) && bytes.Equal(third[51], frames[51]) {
		t.Fatal("another sample wrote the same stream")
	}
	names := header.GetOperationNames()
	counts := map[string]int{}
	authorization := ""
	for i, op := range ops {
		name := names[op.GetOperation()]
		counts[strings.SplitN(name, "_", 2)[0]]++
		steps := op.GetSteps()
		update := strings.HasPrefix(name, "update_read_")
		switch {
		case update && (len(steps) < 2 || len(steps) > 3):
			t.Fatalf("operation %d (%s): %d steps", i, name, len(steps))
		case !update && len(steps) != 1:
			t.Fatalf("operation %d (%s): %d steps", i, name, len(steps))
		}
		if op.GetKeepBodies() {
			t.Fatalf("operation %d (%s) keeps its bodies in a benchmark's phase", i, name)
		}
		service := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name, "update_read_"), "list_"), "read_")
		for _, step := range steps {
			if !strings.HasPrefix(path(step), "/api/v1/"+service+"service/") {
				t.Fatalf("operation %d (%s) sends %s", i, name, path(step))
			}
			var auth string
			for _, header := range step.GetHttp().GetHeaders() {
				if header.GetName() == "Authorization" {
					auth = header.GetValue().GetParts()[0].GetLiteral()
				}
			}
			if authorization == "" {
				authorization = auth
			}
			if !strings.HasPrefix(auth, "Bearer ") || auth != authorization {
				t.Fatalf("operation %d (%s): Authorization %q", i, name, auth)
			}
		}
	}
	for kind, weight := range map[string]float64{"list": 35, "read": 25, "update": 35} {
		share, want := float64(counts[kind])/n, weight/95
		if share < want*0.95 || share > want*1.05 {
			t.Errorf("%s: %.4f of the stream, want %.4f", kind, share, want)
		}
	}
	captured := proto.Clone(input).(*pb.PhaseInput)
	captured.Capture = true
	_, kept, _ := generate(t, captured, dataset, 100)
	for i, op := range kept {
		if !op.GetKeepBodies() || op.GetOperation() != ops[i].GetOperation() || len(op.GetSteps()) != len(ops[i].GetSteps()) {
			t.Fatalf("operation %d under capture: keep bodies %v, operation %d, want %d", i, op.GetKeepBodies(), op.GetOperation(), ops[i].GetOperation())
		}
	}
}
