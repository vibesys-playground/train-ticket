module vibesys/train-ticket-k8s-evaluator

go 1.25.0

// The vibesys-evaluator Go SDK (go/workload for the generators, go/check for the oracle) of the
// release whose PyPI package the task image installs (the Dockerfile's
// VIBESYS_EVALUATOR_VERSION; tests/test_pins.py checks they agree).
require (
	github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go v0.3.0-rc.3
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.9-20250912141014-52f32327d4b0.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.24.0 // indirect
	github.com/anishathalye/porcupine v1.3.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/text v0.26.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250603155806-513f23925822 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250603155806-513f23925822 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
