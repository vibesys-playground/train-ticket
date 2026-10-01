# Train Ticket Kubernetes assets

This package deploys the six Train Ticket services exercised by the
ServiceBench workload, plus their MongoDB dependencies.
Every evaluation builds the six Java modules from the assembled candidate
checkout. The trusted multi-stage `service.Dockerfile` runs Maven inside the build and
does not consume pre-existing `target` artifacts.
It is not named `Dockerfile` because VibeSys treats a task-level `Dockerfile`
as the run-environment image, which cannot be combined with
`--run-environment local`.
Its Maven layer packages all six modules before the module-specific build
argument is declared, so Docker reuses one reactor build for the six runtime
images.

The image builds use the repository root (the candidate directory) as the
Docker build context, and the config expects kind cluster `vibesys-k8s-train`
and context `kind-vibesys-k8s-train`. The first lifecycle
start builds and loads the images. Namespace resets in the same evaluator reuse
those exact images, while a new evaluator process rebuilds the candidate.

`generate_manifest.py` is the source of truth: it deterministically regenerates
the namespace-neutral `manifest.yaml`, and a unit test fails if the committed
file differs from its output. Edit the script, then rerun it. The runtime injects its owned namespace and forwards all six service
endpoints for direct semantic checking and benchmarking.

## Open problem: not yet run live

The manifest provisions `mongo:3.4` per service and injects no environment
variables. This fork's `vibesys` branch is based on upstream
`350f62000e6658e0e543730580c599d8558253e7`, whose six services use Spring Data
MongoDB (hosts `ts-<name>-mongo`, port 27017), so the manifest matches the
source. Later upstream revisions moved to MySQL and Nacos, which this manifest
does not provide, so do not rebase the branch onto them without regenerating
the manifest and the packaged oracle. This configuration has not been run on a
cluster. Do not treat it as working until it is validated on one.
