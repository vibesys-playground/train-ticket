# train-ticket-k8s

Train Ticket's config, station, train, travel, route and price services on Kubernetes,
measured by [vibesys-evaluator](https://github.com/uw-syfi/vibesys/tree/main/sdk/vibesys-evaluator):
closed-loop throughput (`operations_per_second`) and open-loop p99 latency at 70% of it
(`latency_ms.p99`) of a mixed stateful workload; and judged by an accuracy check, the
evaluator's accuracy run with the task's model-based oracle. What the candidate may change and
the correctness bar are in [OBJECTIVE.md](OBJECTIVE.md).

| File | What it is |
| --- | --- |
| `vibesys.input.toml` | The task (schema `"0.2"`, no `[candidate]` server: the task deploys the application itself); `[environment] needs = ["kubernetes"]`. |
| `objectives.toml` | The two objectives, each with the noise floor its base measure gave ("Measured (base)"). |
| `Dockerfile` | The task image: a digest-pinned `python:3.12.12-slim-bookworm` with the PyPI package `vibesys-evaluator==0.3.0rc3` (bundling loadgen, otelcapture and the core `vibesys-evaluator`), kubectl and buildctl, plus the task's Go programs: `tt-generator`, `tt-seed`, `tt-accuracy`, `tt-oracle`. |
| `bench.py` | The benchmark (`python /task/bench.py benchmark --project-root ROOT --vs-output F`): the system (`k8s.App`), the seed, the workload (`Generator(["tt-generator"])`), `benchmark(run)` and the validity rules; and the accuracy check (`python /task/bench.py accuracy --project-root ROOT`): the evaluator's accuracy run of the same system with its own workload (`tt-accuracy`), the load, the fault schedule and the oracle (`tt-oracle`). |
| `dataset.py` | The fixture's seed, size and digest (the fixture itself is generated in Go). |
| `k8s/manifest.yaml` | The topology: six services (the candidate's image), six MongoDB 3.4, the nginx gateway. |
| `evaluator/` | The Go module (`go test ./...`): `internal/ttdata` (fixture, entities, paths, admin JWT), `internal/ttworkload` (the benchmark's operations and model, on the Go SDK's `workload`), `internal/ttapi` (the API both the accuracy workload and the oracle speak), `internal/ttcatalog` (the services' startup catalog), `internal/ttaccuracy` (the accuracy workload), `internal/ttoracle` (the oracle's model, on the Go SDK's `check`), `internal/ttfake` (an in-memory Train Ticket for the tests); `cmd/generator` (`tt-generator`), `cmd/seed` (`tt-seed`), `cmd/accuracy` (`tt-accuracy`), `cmd/oracle` (`tt-oracle`). |
| `tests/` | Python tests of bench.py (the benchmark and the accuracy check), the manifest and the pins. |

## How a sample runs

`bench.py` is a `vibesys_evaluator.microservices.main`. With the cluster vibesys-cli's
`kubernetes` resource gives (`KUBECONFIG`, `VIBESYS_K8S_REGISTRY`, `VIBESYS_K8S_BUILDER`),
the package builds the candidate's image from the repository root (`Dockerfile`, the
Maven reactor as context) and the task image, creates a fresh namespace, applies
`k8s/manifest.yaml`, waits until every Deployment is available and each service answers
its collection through the gateway, then runs the seed Job: `tt-seed` creates the fixture
(32 records of 7 connected entities) through the public API and reads every entity back.
`benchmark(run)` then runs six phases, each a pod of the task image in the namespace where
loadgen reads `tt-generator generate`'s request stream:

1. three closed loops, 128 clients, 30 s windows (the first after a 180 s warmup that warms
   the JVMs, the others after 10 s): `operations_per_second` is the median;
2. three open loops at 0.7 x that, uniform arrivals, 30 s windows after 10 s warmups:
   `latency_ms.p99` is the median.

The application keeps the updates from phase to phase; nothing is reset, and each phase's
generator starts from the fixture. The namespace is deleted on every exit path.

## The workload

`internal/ttworkload` ports the servicebench Train Ticket benchmark adapter (vibesys
`resources/evaluators/microservice/apps/trainticket` and `appsupport/trainticket`, deleted
with servicebench): the same fixture grammar (`fuzz.go`, now on the evaluator's `Rand`, so
the fixture is a function of `dataset.py`'s seed), the same operations and weights and the
same update grammar. The benchmark only measures: whether the answers are right is the
accuracy check's to judge (below). Things that changed with the framework:

- servicebench, and the evaluator before its Go phase runner, held an exclusive key per
  fixture record, so no two operations of one record overlapped. Here requests run freely,
  updates of one entity included; this lowered the closed loop's throughput, which is the
  application's own behavior (below, "No per-record lock").
- Each phase's generator starts from the fixture, while the application keeps the earlier
  phases' updates: a read by a secondary key an earlier phase moved (a station's name, a
  route's direction, a price's route) may find nothing, which the services answer with
  application status 0 in an HTTP 200, a completed operation.
- `create_read_delete_config` (weight 5 of 100) is not in the mix yet; `workload.HTTP("DELETE",
  ...)` can now send it. The mix is 35/25/35 out of 95.
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

## No per-record lock (the closed loop's drop)

Without the exclusive key the closed loop does less: on vibesys-k8s, 4515 and 4889 ops/s in
two closed loops (180 s and 10 s warmups) against 5348 and 5547 of the same schedule with the
key, and `update_read_route` takes 259 ms (p50, second loop) where it took 83 once its key was
free. The open loop at the earlier sample's 3744 ops/s did not change (p99 6.98 ms against 6.76
to 7.50). Whether the key hid contention that the workload makes by updating only 32 routes
(each about 9 times a second in the closed loop) decides the remedy, a wider pool of routes if
so. It did not:

- **No hot-key contention.** For each update, the latency of its write (step 1) against what
  was in flight when it started: the requests of its service, and the writes of the same entity.
  At equal load the same-entity writes change nothing, in every service. In the second closed
  loop, route writes that started with 80 to 100 route requests in flight took 97.6, 98.9 and
  97.2 ms (p50) with 0, 1 and 2 or more writes of their route in flight (1377, 1190 and 744
  writes), and 109.6, 105.6 and 105.2 ms above 100 in flight (1968, 2061, 1719); the first loop
  and the five other services likewise. (Bucketing by the writes that overlap an update over
  its whole length instead makes slow updates look contended, because a longer update overlaps
  more.) A route update is a `findById` and a `save` of one document, and MongoDB's
  document-level concurrency makes overlapping saves of one document cheap.
- **What the key did was admission control.** The route service is the bottleneck: its 2 CPUs
  are saturated in the closed loop with or without the key. With 32 records the key admitted at
  most 32 route updates into it and parked every other client that drew one outside the system
  (104 ms, p50, in the second loop). Without it those clients are inside the route service: 115
  route requests in flight on average (78 in updates) against 45 (26) with the key. Each request
  takes longer (Little's law), and the service does less per CPU at that concurrency: 2.26 and
  1.50 ms of route CPU per route request in the two loops against 1.59 and 1.19 with the key
  (likely the JVM's 200 MB heap: every listing and route search loads the whole collection with
  `findAll`, and two and a half times as many are in flight). That is how this application
  behaves with 128 clients, and something a candidate can improve.
- **A wider pool would not change it.** How many route requests are in flight is set by the
  clients and the mix, not by which routes they name; with the key gone, more routes would only
  grow the collection that every listing and route search reads.

So the workload keeps its keys; the drop is part of the base's numbers ("Measured (base)":
4818 ops/s over five samples).

## The accuracy check (`[accuracy]`)

`bench.py accuracy` is the evaluator's accuracy run (vibesys-evaluator `docs/spec.md`,
"Accuracy run"): it deploys and seeds the candidate exactly as the benchmark does (`bench.SYSTEM`), runs
four open-loop phases of the accuracy workload (60 s each after a 5 s warmup, Poisson arrivals
at 40 operations per second) with every request and response captured, restarts the services
between phases (the fault schedule), deletes the namespace, and runs the oracle (`tt-oracle`)
on the history. The exit status is the verdict's: 0 PASS, 1 FAIL, 2 UNKNOWN (also when the
run could not complete); `$VIBESYS_ARTIFACTS_DIR/accuracy/` holds the report, the failures'
timelines and the history, which `tt-oracle --history DIR` re-checks offline. The check
carries its own workload (`ms.Accuracy(workload=...)`), not the benchmark's; it is a constant
of bench.py, because the phase pods rebuild the task without the orchestrator's environment.

### The workload (`internal/ttaccuracy`)

Thirty-four operations, one request each, of the six services' public API through the gateway
with the admin token, each carrying the `ttapi.Info` the oracle reads back: creates, updates,
deletes, route and price upserts (a few with server-generated ids), reads by key, the six
listings, the secondary-key queries (station id by name, station name by id, routes between two
stations, a price by route and train type, trips by route) and the two trip joins (a trip's
route and train). About 46% writes and 12% listings. Each key is drawn from three pools per
table: the seeded fixture (60%), the startup catalog (10%, which restarts write again), and
eight ids of the check's own (30%, so that writes of one key follow each other closely and
reads see them). The stream is generated ahead, independent of the answers.

### The fault schedule

Every service is restarted once, between phases, when no request is in flight:

| After phase | Restarted |
| --- | --- |
| 0 | `ts-route-service`, `ts-price-service` |
| 1 | `ts-station-service`, `ts-config-service` |
| 2 | `ts-train-service`, `ts-travel-service` |

This is what the oracle's `Fault` models: a restart of `ts-<table>-service` keeps every
acknowledged write of its table and re-applies the service's startup catalog (create-if-absent
for configs, stations, trains and trips; an overwrite for routes and prices), and touches no
other table. Routes and prices go first, so the overwrite meets a phase's worth of changes to
the catalog's routes and prices; trips last, after the routes and trains their joins read
have been restarted. A fault the model does not describe is not scheduled: restarting a
MongoDB (it has no volume, so its data would go), or a restart under load (a write in flight
at the kill is lost or not, which the oracle could only call unknown).

### The load

Fixed, so that the history and the check's cost do not depend on the candidate's speed, and low,
so that answers are judged rather than capacity: 40 operations per second, about 10400
operations in all (the warmups are checked too). On the base at that rate no answer is lost, and at most 2 operations are in
flight on one key, far below the checker's warning at 12 (measured below).

### The oracle (`cmd/oracle`, `internal/ttoracle`)

Train Ticket's semantics as plain Go over per-entity state, on vibesys-evaluator's `go/check`
package (authoring guide: vibesys-evaluator `docs/accuracy-checks.md`). `cmd/oracle` is
`check.Main`. `internal/ttapi` holds what the oracle and the workload share: the operation
`Info`, the HTTP request each operation maps to, strict entity schemas and the response parser.

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
- **What exists** (`ForFixture`): when the history starts, the startup catalog
  (`internal/ttcatalog`, the base's InitData: 1 config, 13 stations, 6 trains, 10 routes,
  10 prices, 5 trips) and the seeded fixture; a restart re-applies the catalog.
- **Calls with nothing to check against** (an unknown read, a lost generated-id upsert no key
  could come from, a query nothing could have matched) become pieces without keys: counted, an
  unknown one toward the checker's 1% bound on lost answers, and not searched.

Tests (`go test ./...` in `evaluator/`): the accuracy workload against the in-memory fake
(`internal/ttfake`, seeded with the catalog and the fixture, re-seeding the catalog on restart)
with the fault schedule between phases, which the oracle accepts, and with five injected bugs
(a lost update, a stale secondary index, a delete that a restart undoes, a wrong schema field, a
listing that drops its last element), which it fails; the generator binary under capture;
synthetic concurrent histories with lost responses and restarts; seven injected bugs and two
proxy mutants; hand-written racy-create, racy-delete and dangling-join histories; and
`testdata/real-capture.json.gz`, a history recorded against the real services on kind (6
clients, 3 phases, two rolling restarts), checked offline.

### Measured (base and two broken candidates)

On vibesys-k8s-2 (a shared kind cluster), with vibesys-evaluator 0.3.0rc2, one run each (each
run draws its own seed):

| Candidate | Verdict | Wall time | Calls (lost) | Pieces | Keys | Most in flight on a key | Check |
| --- | --- | --- | --- | --- | --- | --- | --- |
| The base | PASS | 7.4 min | 10293 (0) | 92418 | 411 | 2 | 1.7 s |
| Config updates kept in memory only | FAIL: 20 config keys (after the config restart a listing shows the value before the update) | 7.1 min | 10511 (0) | 95693 | 404 | 2 | 2.0 s |
| Trains cached by id, updates not invalidating | FAIL: 36 train keys (a read after an update shows the old train) | 7.2 min | 10359 (0) | 93575 | 391 | 2 | 1.8 s |

Every phase answered every request with a 2xx (about 2580 calls a phase: 7% creates, 12% deletes,
19% updates, 7% upserts, 20% reads by key, 11% listings, 23% queries and joins). The faults
are exercised: before its service's restart the workload changed catalog entities of every
table (in one run of the base, 7 deletes and 19 upserts of catalog routes before the route
restart, 10 deletes and 21 updates of catalog configs before the config restart, and so on),
and the listings, reads and queries after it see what the restart re-seeded. The restarts took
15 to 17 s each, and a run about 7 minutes: about 1 to deploy and seed, 4.3 of load, 1.6 of
restarts, seconds to check.

## Resources

Requests equal limits. The application: 6 services x (2 CPU, 1 GiB), 6 MongoDB x (1 CPU,
1 GiB), the gateway (2 CPU, 256 MiB): 20 CPUs and about 12.3 GiB. Each phase pod adds the
package's 6 CPUs and 4 GiB, the seed pod 4 CPUs and 2 GiB (not at the same time): about 26
CPUs and 16.3 GiB at peak. A benchmark sample takes about 8 minutes, an accuracy check about
7 (the images build in about a minute with warm caches).

## Measured (base)

On vibesys-k8s (a shared kind cluster: one node on 32 of the host's 64 CPUs, which jobs outside
the cluster also use), with vibesys-evaluator 0.3.0rc2, five samples of the base, each its own
deployment, seed and namespace; every phase valid (no request error, every operation of the mix
completed, the open loops offered their rate), seeded in 7 to 10 s, 8.3 to 8.6 minutes a sample:

| Sample seed | Closed loops (ops/s) | Open loops (rate: p99 ms) | Result |
| --- | --- | --- | --- |
| 5472255961397713206 | 4443, 4791, 4799 | 3354: 7.01, 6.95, 8.10 | 4791.3 ops/s, 7.01 ms |
| 1311371194280895585 | 4666, 4854, 4948 | 3398: 7.46, 649.22, 7.47 | 4854.0 ops/s, 7.48 ms |
| 3006667050301291512 | 5147, 4762, 4840 | 3388: 7.65, 7.20, 7.27 | 4839.7 ops/s, 7.27 ms |
| 3489661498922462808 | 4994, 4726, 4717 | 3308: 7.71, 18.94, 21.60 | 4726.0 ops/s, 18.94 ms |
| 8229105765312355147 | 4880, 5414, 4799 | 3416: 6.80, 1199.10, 6.88 | 4879.5 ops/s, 6.88 ms |

- `operations_per_second`: mean 4818 ops/s, standard deviation 61 (1.3% of the mean).
  `objectives.toml` declares `relative_noise = 0.02`.
- `latency_ms.p99`: median 7.27 ms; four samples within 6.9 to 7.5 ms (mean 7.16, standard
  deviation 3.7%), and one at 18.9 ms (the five: mean 9.5, deviation 55%). `objectives.toml`
  declares `relative_noise = 0.05`, the clean samples' spread rounded up: a floor, which the
  runs' own pooled spread replaces when it is larger, as it is when a sample like the fourth
  is among them.

The p99's outliers come from stalls. Four of the fifteen open-loop trials were hit. In two (649
and 1199 ms) every service slowed past 100 ms at the same instant, then the train and config
services stalled for 1 to 1.6 s and drained their backlog over the next 2 to 8 s; whether the
host starts these (every service at once) or the two JVMs' own pauses prolong them, these runs
cannot tell. In the fourth sample's last two (19 and 22 ms) every operation's tail rose evenly
(p90 2 to 3 times the first trial's) with no stall, as under contention from the host's other
jobs. The median of three absorbs one such trial, not two: about one sample in five. Five
open-loop trials would make it rarer, at 80 s more a sample. The throughput's first trial,
after the 180 s warmup, varies more (4443 to 5147) than the median of three does.

Before the phase runner (with the per-record key, "No per-record lock"), the same schedule
measured 5348 ops/s and 7.07 ms on one sample; with a 30 s first warmup the closed loop was still
warming up (the throughput climbed from 1300 to 5300 ops/s over 170 s of load while the route
service ran at its 2-CPU limit and its reads got faster), which is why the first warmup is 180 s.

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

The accuracy check is the same `docker run` with `python /task/bench.py accuracy
--project-root "$PWD"`; `tt-oracle --history /tmp/tt-artifacts/accuracy/history` (in the task
image) re-checks its history.

## Tests

```bash
(cd .vibesys/tasks/train-ticket-k8s/evaluator && go test ./...)
python -m pytest .vibesys/tasks/train-ticket-k8s/tests   # needs vibesys-evaluator==0.3.0rc3, pyyaml, pytest
```

The Go tests cover the fixture (pinned by `dataset.py`'s digest), the admin token, request
building, the update grammar, the generator's stream (deterministic, in the mix's
proportions, bodies kept only under capture), the seed against a fake API, the manifest
rendered by the Go SDK's renderer, and the accuracy check (above). The Python tests build
bench.py's objects (the benchmark's and the accuracy check's), drive `benchmark(run)` with a
fake run, check the `[accuracy]` command, and check that the Dockerfile's base image and
`evaluator/go.mod` pin the same vibesys-evaluator release.
