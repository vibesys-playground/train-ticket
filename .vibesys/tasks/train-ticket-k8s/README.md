# train-ticket-k8s

Train Ticket's config, station, train, travel, route and price services on Kubernetes,
measured by [vibesys-evaluator](https://github.com/uw-syfi/vibesys/tree/main/sdk/vibesys-evaluator):
closed-loop throughput (`operations_per_second`) and open-loop p99 latency at 70% of it
(`latency_ms.p99`) of a mixed stateful workload. What the candidate may change and the
correctness bar are in [OBJECTIVE.md](OBJECTIVE.md).

| File | What it is |
| --- | --- |
| `vibesys.input.toml` | The task (schema `"0.2"`, no `[candidate]` server: the task deploys the application itself); `[environment] needs = ["kubernetes"]`. |
| `objectives.toml` | The two objectives; the noise values are placeholders until a base is measured. |
| `Dockerfile` | The task image: vibesys-evaluator (the Python package from PyPI, loadgen and otelcapture by `go install`) at the release `VIBESYS_EVALUATOR_VERSION` (v0.2.0-rc.1), kubectl, buildctl, the task's Go programs, and the interim check's servicebench. |
| `bench.py` | The benchmark (`python /task/bench.py benchmark --project-root ROOT --vs-output F`): the system (`k8s.App`), the seed, the workload (`Generator(["tt-generator"])`), `benchmark(run)` and the validity rules. |
| `dataset.py` | The fixture's seed, size and digest (the fixture itself is generated in Go). |
| `k8s/manifest.yaml` | The topology: six services (the candidate's image), six MongoDB 3.4, the nginx gateway. |
| `evaluator/` | The Go module: `internal/ttdata` (fixture, entities, paths, admin JWT), `internal/ttworkload` (operations, model, checks, on `loadgen/workload`), `cmd/generator` (`tt-generator`), `cmd/seed` (`tt-seed`). |
| `check.py`, `accuracy/workload.toml` | The interim `[accuracy]` check (below). |
| `tests/` | Python tests of bench.py, check.py, the manifest and the pins. |

## How a sample runs

`bench.py` is a `vibesys_evaluator.microservices.main`. With the cluster vibesys-cli's
`kubernetes` resource gives (`KUBECONFIG`, `VIBESYS_K8S_REGISTRY`, `VIBESYS_K8S_BUILDER`),
the package builds the candidate's image from the repository root (`Dockerfile`, the
Maven reactor as context) and the task image, creates a fresh namespace, applies
`k8s/manifest.yaml`, waits until every Deployment is available and each service answers
its collection through the gateway, then runs the seed Job: `tt-seed` creates the fixture
(32 records of 7 connected entities) through the public API and reads every entity back.
`benchmark(run)` then runs six phases, each a pod of the task image in the namespace where
loadgen reads `tt-generator generate`'s request stream and `tt-generator check` checks the
responses afterwards:

1. three closed loops, 128 clients, 30 s windows (the first after a 180 s warmup that warms
   the JVMs, the others after 10 s): `operations_per_second` is the median;
2. three open loops at 0.7 x that, uniform arrivals, 30 s windows after 10 s warmups:
   `latency_ms.p99` is the median.

Updates carry from phase to phase (the history); nothing is reset. The namespace is
deleted on every exit path.

## The workload

`internal/ttworkload` ports the servicebench Train Ticket benchmark adapter (vibesys
`resources/evaluators/microservice/apps/trainticket` and `appsupport/trainticket`, deleted
with servicebench): the same fixture grammar (`fuzz.go`, now on the evaluator's `Rand`, so
the fixture is a function of `dataset.py`'s seed), the same operations and weights, the same
update grammar, and the same response validation (`validation.go`: the strict envelope, exact
field sets and types of list elements, unique keys, exact values, negative reads of retired
secondary keys). Two things changed with the framework:

- servicebench held a lock per fixture record, so a read never overlapped an update of its
  record and saw exactly the last value. Here an update holds its entity's exclusive key
  (updates of one entity never overlap and run in stream order) and reads run freely; a read
  is checked against the values the visibility rule allows (`workload.Versions` with bound 0:
  an update acknowledged before the read started must be visible; one concurrent with it may
  be). Writes are always checked; reads are checked on the SDK's default 10% sample.
- `create_read_delete_config` (weight 5 of 100) is not in the mix: it needs an HTTP DELETE,
  which neither SDK front end can send yet (below). The mix is 35/25/35 out of 95.
- The fixture is fixed by `dataset.py` (servicebench drew a fresh one per run); request
  seeds are drawn per sample.

## What vibesys-evaluator lacks (worked around or left out here)

- **One Dockerfile, several images (build arguments).** `k8s.App(images=...)` maps a manifest
  variable to a candidate directory that is both the build context and the Dockerfile's
  directory (`<dir>/Dockerfile`), tagged by the directory's digest. It has no build
  arguments, no Dockerfile path apart from the context, and two variables of one directory
  would get one tag. So the earlier layout (one trusted Dockerfile, `MODULE` build argument,
  one image per service) cannot be expressed. The task works around it without touching the
  evaluator: the candidate's image holds every service and each Deployment runs it with its
  own `command`. A minimal change that would allow the earlier layout: let an `images` value
  be `k8s.Image(context=".", dockerfile="path/Dockerfile", args={"MODULE": "..."})` besides a
  directory string, include the Dockerfile and the arguments in the tag's digest, and pass
  `--opt filename=...`, `--local dockerfile=...` and `--opt build-arg:K=V` to buildctl.
- **HTTP DELETE.** The schema (`HTTP_METHOD_DELETE`) and loadgen support it; the Go SDK has
  only `Get`/`Post`/`Put` and the Python SDK `get`/`post`/`put`. Adding `workload.Delete` and
  `microservices.delete` (same options as `Put`) would let the mix get its create, read,
  delete, negative-read operation back.

## The accuracy oracle (`evaluator/cmd/oracle`, not wired yet)

The model-based replacement for the interim check: Train Ticket's semantics as plain Go over
per-entity state (`evaluator/internal/ttoracle`), on vibesys-evaluator's `go/check` package
(authoring guide: `sdk/vibesys-evaluator/docs/accuracy-checks.md`). `cmd/oracle` is
`check.Main`: `oracle --history DIR [--out DIR]` re-checks a captured history offline and exits
0 (PASS), 1 (FAIL) or 2 (UNKNOWN). `internal/ttapi` holds what the oracle and the future
accuracy workload share: the operation `Info` the workload must emit, the HTTP request each
operation maps to, strict entity schemas and the response parser.

- **Keys** are entities (`station/shanghai`, `route/<id>`, `travel/G1234`). A listing, a
  secondary-index query (route between two stations, trips by route, price by route and train,
  station id by name) or a trip-to-route/train join is projected onto the keys it could have
  returned: per-key views, no whole-collection snapshots (Mongo's `findAll` is not one).
- **The model is the application as it is** (found on the real deployment): a restart re-applies
  the service's seed (create-if-absent; an upsert for route and price); config/station/train
  create is find-then-save, so overlapping creates can both succeed; route and price POST are
  upserts with server-generated ids for a short id; route DELETE answers 0 when a concurrent
  upsert brought the route back; duplicate trip create and update of a missing trip answer
  status 1 and change nothing; the trip-to-route join answers an empty route when the route is
  gone and carries only id, stations and distances.
- **Statuses**: 2xx known; 5xx or a lost response unknown (the write may or may not have taken
  effect, within 10 s); any other status, a malformed body or a schema violation fails the check
  without a search.
- **Dataset** (`dataset.json` in the history): `{"initial": {table: [entity]}, "reinit": ...}`,
  both in the shape of `seed_catalog.json` (`initial`: what exists when the history starts,
  the seed Job's fixture included; `reinit`: what a restart re-applies, default `initial`).

Tests (`go test ./...` in `evaluator/`): synthetic concurrent histories of an in-memory fake
(`internal/ttfake`) that the oracle accepts, with lost responses, with restart re-seeding; seven
injected bugs and two proxy mutants (stale reads, a dropped field) that it fails; hand-written
racy-create and dangling-join histories; and `testdata/real-capture.json.gz`, a history recorded
against the real services on kind (6 clients, 3 phases, two rolling restarts), checked offline.

What the accuracy command will look like once the evaluator's accuracy flow exists (it deploys,
seeds, runs the phases at a fixed open-loop rate with the restarts between them, and captures
the history): `[accuracy] command = ["tt-oracle", "--history", "${HISTORY_DIR}"]` with the task
image building `tt-oracle` from `evaluator/cmd/oracle`. Nothing in this directory calls it yet:
`check.py` stays the `[accuracy]` command until then, and the accuracy workload (which must
emit `ttapi.Info` as the operations' info, one step per operation) is written with the flow.

## The interim check (`[accuracy]`)

The task's own correctness check is still to be designed. Until then, `check.py` deploys the
candidate exactly as the benchmark does and, in place of the benchmark's seed, runs
servicebench's Train Ticket accuracy suite (`servicebench --mode accuracy`, built in the task
image from `ORACLE_VIBESYS_COMMIT`, the last vibesys commit with servicebench) in the seed
Job against the gateway; no load phase runs. It checks the startup catalog, exact schemas, a
randomized create/read/update/delete graph, read your write, deletes and stale secondary
indexes (no crash recovery: the suite's restart hook is not wired). Exit 0 means the suite
passed. On the cluster, against the base, it passed in 2.2 minutes (the suite 46 s in the seed
Job: 34570 checks, 32 random cases).

## Resources

Requests equal limits. The application: 6 services x (2 CPU, 1 GiB), 6 MongoDB x (1 CPU,
1 GiB), the gateway (2 CPU, 256 MiB): 20 CPUs and about 12.3 GiB. Each phase pod adds the
package's 6 CPUs and 4 GiB, the seed pod 4 CPUs and 2 GiB (not at the same time): about 26
CPUs and 16.3 GiB at peak. One sample takes about 22 minutes (the images build in about a
minute with warm caches): every phase pod receives the sample's whole write history, and the
first warmup's 336 thousand writes (235 MB) make each later 40 s phase take about 3 minutes.

## Measured (base)

On the shared kind cluster (one 32-CPU node, sharing the host with other jobs), every phase of
both samples valid (0 errors, 0 failed checks), seeded (224 entities, 448 requests) in 8 s:

| Sample | First warmup | Closed loops (ops/s) | Open loops (rate, p99 ms) | Result | Time |
| --- | --- | --- | --- | --- | --- |
| 3ce8dbbf | 30 s | 3392, 4013, 5148 | 2809: 7.01, 6.88, 7.25 | 4013.2 ops/s, 7.01 ms | 11 min |
| bac5b6c5 | 180 s | 5348, 5547, 4811 | 3744: 7.07, 7.50, 6.76 | 5347.9 ops/s, 7.07 ms | 22 min |

With a 30 s warmup the closed loop was still warming up: the throughput climbed from 1300 to
5300 ops/s over 170 s of load while the route service ran at its 2-CPU limit and its reads got
faster (p50 94 ms to 20 ms); the other services used 1 to 1.5 CPUs and each MongoDB about 0.15.
After 180 s it no longer climbs within a window (5200 to 6200 ops/s in 5 s buckets of the
second trial); the third trial's dip (3900 to 5700 ops/s, every service slower and the route
service below its limit) came with a host load average of 85 on 64 CPUs from jobs outside the
cluster. In the closed loop route updates also wait for their route's exclusive key (32 routes,
about 66 ms per update), which is what the closed loop's p99 (230 to 780 ms) shows; the open
loop's p99 is the objective.

## Running it

With vibesys-cli, on a placement with a `kubernetes` resource. The released CLI refuses a
`"0.2"` task without `[candidate]` until the CLI change that makes it optional is released.

By hand, as vibesys-cli runs it (the kubeconfig of a cluster that pulls from the registry,
a BuildKit builder, host networking):

```bash
docker build -t train-ticket-k8s-task .vibesys/tasks/train-ticket-k8s
docker run --rm --network host --user "$(id -u):$(id -g)" \
  -v "$KUBECONFIG:/vibesys/kubeconfig:ro" -e KUBECONFIG=/vibesys/kubeconfig \
  -e VIBESYS_K8S_REGISTRY=localhost:5001 -e VIBESYS_K8S_BUILDER=tcp://127.0.0.1:1234 \
  -v "$PWD:$PWD" -v /tmp/tt-artifacts:/vibesys/artifacts -e VIBESYS_ARTIFACTS_DIR=/vibesys/artifacts \
  train-ticket-k8s-task python /task/bench.py benchmark --project-root "$PWD" \
  --vs-output /vibesys/artifacts/records.jsonl
```

## Tests

```bash
(cd .vibesys/tasks/train-ticket-k8s/evaluator && go test ./...)
python -m pytest .vibesys/tasks/train-ticket-k8s/tests   # needs vibesys-evaluator at the pin, pyyaml, pytest
```

The Go tests cover the fixture (pinned by `dataset.py`'s digest), the admin token, request
building, the update grammar, the checks against canned responses (including the
visibility rule), the generator's stream (deterministic, in the mix's proportions) and the
seed against a fake API. The Python tests build bench.py's and check.py's objects, render
the manifest with the package's renderer, drive `benchmark(run)` with a fake run, and check
that the Dockerfile and `evaluator/go.mod` pin the same vibesys commit.
