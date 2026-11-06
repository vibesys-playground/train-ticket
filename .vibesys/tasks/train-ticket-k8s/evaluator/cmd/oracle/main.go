// Command oracle (tt-oracle in the task image) is train-ticket-k8s's accuracy oracle: it checks a
// captured history of the accuracy workload (internal/ttaccuracy) against Train Ticket's
// semantics (internal/ttoracle), on vibesys-evaluator's checker (go/check).
//
// accuracy.py's accuracy check runs it on the history the accuracy run wrote:
//
//	tt-oracle --history DIR --out DIR
//
// and it exits 0 (PASS), 1 (FAIL) or 2 (UNKNOWN). The history's dataset file is the task's
// (dataset.py: the fixture's seed, size and digest): the oracle's initial state is the services'
// startup catalog (internal/ttcatalog) and that fixture, and a restart re-applies the catalog.
// `tt-oracle import ...` makes a history directory of older phase directories (check.Main).
package main

import (
	"errors"

	"vibesys/train-ticket-k8s-evaluator/internal/ttdata"
	"vibesys/train-ticket-k8s-evaluator/internal/ttoracle"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/check"
)

func main() {
	check.Main(func(h *check.History) ([]check.AnySpec, error) {
		if h.Dataset == "" {
			return nil, errors.New("the history has no dataset file")
		}
		data, err := ttdata.Load(h.Dataset)
		if err != nil {
			return nil, err
		}
		d, err := ttoracle.ForFixture(data)
		if err != nil {
			return nil, err
		}
		spec, err := ttoracle.Spec(d)
		if err != nil {
			return nil, err
		}
		return []check.AnySpec{spec}, nil
	})
}
