# VibeSys tasks

This fork of [FudanSELab/train-ticket](https://github.com/FudanSELab/train-ticket)
carries repository-native VibeSys tasks. The `vibesys` branch is based on
upstream `350f62000e6658e0e543730580c599d8558253e7`, whose services store data
in MongoDB; the tasks target that API contract. Later upstream revisions moved
to MySQL and Nacos and use different entity identifiers: do not rebase the
branch onto them without reworking the task.

| Task | Target |
| --- | --- |
| [`train-ticket-k8s`](tasks/train-ticket-k8s/) | The config, station, train, travel, route and price services, built from this repository into one image and deployed with their MongoDB databases to a Kubernetes cluster; throughput and p99 latency of a mixed stateful workload. |

A task is a directory under `tasks/` in the vibesys-cli task format (schema
`version = "0.2"`): a `Dockerfile` for the task image, `vibesys.input.toml`,
`objectives.toml`, `OBJECTIVE.md` and the trusted code the image runs. The
benchmark is built on
[vibesys-evaluator](https://github.com/uw-syfi/vibesys/tree/main/sdk/vibesys-evaluator)
(the Python package `vibesys_evaluator.microservices` deploys and orchestrates;
the workload is a Go generator on the package's Go SDK). The task needs a
placement with a `kubernetes` resource (a kubeconfig, an image registry and a
BuildKit builder); see the task's README for running it.

Task definitions are authored input. VibeSys owns generated run data under
`state/`; local-only data under `state/local/` is ignored by Git.

The earlier tasks, `default` (a running deployment, usually Docker Compose,
with `scripts/start-local-cluster.sh`) and `kubernetes` (the servicebench
`kubernetes-runtime`), were removed with the servicebench evaluator they ran
on; they are in the history of this branch.
