// Command generator is the train-ticket-k8s benchmark's request generator: the workload of
// internal/ttworkload on the vibesys-evaluator Go SDK. The phase pod runs it under loadgen as
// `tt-generator generate ...` and after the phase as `tt-generator check ...` (the generator
// command line of the SDK's docs/spec.md); bench.py names it.
package main

import (
	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/workload"

	"vibesys/train-ticket-k8s-evaluator/internal/ttworkload"
)

func main() { workload.Main(ttworkload.Workload()) }
