module vibesys/train-ticket-k8s-evaluator

go 1.25.0

// The vibesys-evaluator Go SDK of the commit whose loadgen and Python package the task image
// installs (the Dockerfile's VIBESYS_COMMIT; tests/test_pins.py checks they agree).
require (
	github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen v0.1.0-rc.2.0.20261106013004-5b13578e0d29
	google.golang.org/protobuf v1.36.12
)

require buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.9-20250912141014-52f32327d4b0.1 // indirect
