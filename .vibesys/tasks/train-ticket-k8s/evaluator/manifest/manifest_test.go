// Package manifest_test checks k8s/manifest.yaml with the renderer the deploy uses (the SDK's
// k8s.Render): the variables bench.py's k8s.App gives, the kinds a task may apply, one namespace.
package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go/k8s"
)

func TestTheDeployRendersTheManifest(t *testing.T) {
	text, err := os.ReadFile(filepath.Join("..", "..", "k8s", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// bench.py: images={"TRAIN_TICKET_IMAGE": "."}, no data tier.
	images := map[string]string{"TRAIN_TICKET_IMAGE": "registry/vse/train-ticket:test"}
	if _, err := k8s.Render(string(text), "vse-test", images, nil, "k8s/manifest.yaml"); err != nil {
		t.Fatal(err)
	}
}
