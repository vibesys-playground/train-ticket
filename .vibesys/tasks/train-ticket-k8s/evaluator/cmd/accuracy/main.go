// Command accuracy (tt-accuracy in the task image) is the train-ticket-k8s accuracy check's
// request generator: the workload of internal/ttaccuracy on the vibesys-evaluator Go SDK. The
// accuracy run's phase pods run it under loadgen as `tt-accuracy generate ...` (the generator
// command line of the SDK's docs/spec.md); bench.py's accuracy check names it.
package main

import (
	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttaccuracy"
)

func main() { workload.Main(ttaccuracy.Workload()) }
