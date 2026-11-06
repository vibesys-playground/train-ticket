// Command oracle is train-ticket-k8s's accuracy oracle: it checks a captured history of the
// task's accuracy workload against Train Ticket's semantics (internal/ttoracle), on the
// vibesys-evaluator checker (go/check).
//
// The task's [accuracy] command will run it on the history the accuracy capture wrote:
//
//	oracle --history DIR [--out /vibesys/artifacts/accuracy]
//
// and it exits 0 (PASS), 1 (FAIL) or 2 (UNKNOWN). The history's dataset file is dataset.json:
// {"initial": {table: [entity...]}, "reinit": {table: [entity...]}} (see ttoracle.Dataset).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"vibesys/train-ticket-k8s-evaluator/internal/ttoracle"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

func main() {
	check.Main(func(h *check.History) ([]check.AnySpec, error) {
		if h.Dataset == "" {
			return nil, errors.New("the history has no dataset file")
		}
		raw, err := os.ReadFile(h.Dataset)
		if err != nil {
			return nil, err
		}
		d := &ttoracle.Dataset{}
		if err := json.Unmarshal(raw, d); err != nil {
			return nil, fmt.Errorf("%s: %w", h.Dataset, err)
		}
		spec, err := ttoracle.Spec(d)
		if err != nil {
			return nil, err
		}
		return []check.AnySpec{spec}, nil
	})
}
