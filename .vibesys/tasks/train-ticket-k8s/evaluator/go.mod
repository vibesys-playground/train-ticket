module vibesys/train-ticket-k8s-evaluator

go 1.25.0

// The vibesys-evaluator Go SDK of the release whose loadgen and Python package the task image
// installs (the Dockerfile's VIBESYS_EVALUATOR_VERSION; tests/test_pins.py checks they agree).
require (
	github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen v0.2.0-rc.1
	google.golang.org/protobuf v1.36.12
)

// The accuracy oracle's checker (go/check), at a pseudo-version of vibesys main (cdb35bd1f, PR #3961);
// switch to the release tag once go/check is released.
require github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go v0.0.0-20261106083534-cdb35bd1f4f6

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.9-20250912141014-52f32327d4b0.1 // indirect
	github.com/anishathalye/porcupine v1.3.1 // indirect
)
