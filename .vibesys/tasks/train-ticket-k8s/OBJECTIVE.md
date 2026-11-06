# Train Ticket on Kubernetes: throughput and p99 latency

## Goal

Improve Train Ticket's six core services on two objectives at once. Both are measured on
every sample, against a Kubernetes cluster, in two phases:

| Objective | Direction | Phase |
| --- | --- | --- |
| `operations_per_second` | max | Closed loop, 128 concurrent clients, the mixed stateful workload; the median of three trials. |
| `latency_ms.p99` | min | Open loop, the same mix, offered at 70% of the throughput the same sample's closed loop just measured; the median of three trials. |

The two together form a Pareto frontier: a change that raises throughput and lowers tail
latency is a win; a change that trades one for the other is a tradeoff, not a promotion.

## Workload

Requests go through the task's gateway to the services' public HTTP API
(`/api/v1/<name>service/...`), all with an admin JWT. Before the phases, the task creates a
fixture of 32 connected records through that API: per record a config, two stations, a
train, the route between the stations, a price of the train on the route and a trip.
Each operation is on one record, drawn uniformly:

- 35% lists: a service's whole collection (configs, stations, trains, trips, routes,
  prices);
- 25% reads by key: a config by name, a station's name by its id, a train, a trip, a route,
  a price by its route and train type;
- 35% updates, each followed by a read of the same entity (read your write) and, where the
  update moved a secondary key, a read by the retired key that must find nothing: a
  config's value, a station's name and stay time (then its id by the old name), a train's
  speed and seats, a trip's end time, a route's direction and distance (then a route search
  by the old start and terminal), a price's route and rates (then a price lookup by the old
  route and train).

An operation is one logical operation of one to three HTTP requests; its latency is the
whole sequence's. Every sample draws fresh request seeds, so request streams differ between
samples; the fixture is the task's (`dataset.py`).

## Scope

You may change the Java services of the six modules (`ts-config-service`,
`ts-station-service`, `ts-train-service`, `ts-travel-service`, `ts-route-service`,
`ts-price-service`) and what they share (`ts-common`, the root `pom.xml`), and the root
`Dockerfile` (with `.dockerignore`), which builds the one image that every application pod
runs, with the repository root as its build context. You may rewrite a service in another
language or runtime, as long as the image keeps the contract below. Do not touch
`.vibesys/`.

The task owns everything else, and a candidate cannot change it:

- the Kubernetes manifest (`k8s/manifest.yaml`): one Deployment per service, each with its
  own MongoDB 3.4 (`ts-<name>-mongo:27017`, database `ts`), and the gateway (nginx,
  `ts-gateway:8080`) that routes `/api/v1/<name>service` to `ts-<name>-service`; replica
  counts, resources, commands;
- the fixture, the load generator, the workloads, the open loop's offered rate, and the
  accuracy check.

Resources are fixed (requests = limits). Every service pod gets 2 CPUs and 1 GiB; every
MongoDB 1 CPU and 1 GiB; the gateway 2 CPUs. You cannot add replicas or CPU; a gain must come
from the code using its CPU and its database better.

## Start and readiness contract

The task builds the image from the repository root's `Dockerfile` and starts each service
with `command: [ts-<name>-service]` (an executable of that name on `PATH`), listening on its
port: config 15679, station 12345, train 14567, travel 12346, route 11178, price 16579. A
service is ready when its readiness path answers 200 (`/api/v1/configservice/welcome`,
`/api/v1/stationservice/welcome`, `/api/v1/trainservice/trains/welcome`,
`/api/v1/travelservice/welcome`, `/api/v1/routeservice/welcome`,
`/api/v1/priceservice/prices/welcome`), and the deployment is ready when every Deployment is
available and each service's collection answers 200 through the gateway. A candidate that is
not ready within 300 seconds fails.

Services start from empty databases and write their startup catalog themselves (the
stations, trains, trips, routes, prices and configs of Train Ticket v0.2.0); the task then
creates its fixture through the API.

## Correctness bar

The benchmark only measures; the task's accuracy check judges the answers (README.md, "The
accuracy check"). It deploys the candidate as the benchmark does, sends a mixed workload at a
low fixed rate (creates, updates, deletes, upserts, reads by key, listings, secondary-key
queries and the trip joins, over the services' startup catalog, the fixture and ids of its
own), restarts every service once between its phases, and checks every answer against a model
of Train Ticket's six services as the base implements them:

1. Exact contract: every response is the services' envelope `{status, msg, data}` with
   exactly those fields; an entity has exactly its service's fields with their types
   (lists check every element); keys are unique in a list. A response's HTTP status is 2xx.
2. Exact data: a read or list shows the entity's value exactly as written (strings,
   numbers, epoch-millisecond times, station and distance arrays in order).
3. One order per entity: the answers about each entity are explained by one order of the
   operations on it that respects real time (an operation that completed before another
   started comes first), with each operation's application status (`status` 1 or 0) the
   base's for the entity's state at that point: a create of an existing entity, an update or
   delete of a missing one, and so on. Operations that overlap may be ordered either way. A
   listing, a secondary-key query and a trip join are judged entity by entity (a listing need
   not be a snapshot of the whole collection).
4. Secondary keys: after an update moves a secondary key (a station's name, a route's
   stations, a price's route), a lookup by the retired key no longer finds the entity, and a
   lookup by the new key does.
5. Restarts: a restarted service keeps every acknowledged write. On start it writes its
   startup catalog (the stations, trains, trips, routes, prices and configs of Train Ticket
   v0.2.0) as the base does: what is missing is created, and routes and prices are written
   over.
6. Lost answers: a 5xx or a request with no answer counts as an operation that may or may not
   have taken effect; if more than 1% of what the check judges has such an unknown outcome,
   the check fails (at its low fixed rate, losing answers is the candidate's doing).

The base's services find, then act, without a transaction, so two overlapping writes of one
entity can both succeed where one order would refuse one of them (two creates, two deletes,
an update that saves back an entity an overlapping delete removed). The check accepts these
outcomes while the writes overlap; a candidate need not produce them.

In the benchmark, a request error (a response other than the step's expected HTTP status, or
none) in a measurement window fails the sample, as does an operation of the mix that never
completed or an open loop that cannot offer 95% of its rate.

## Rules

- No caching of benchmark inputs or answers keyed on the workload (the fixture's ids,
  names or values) beyond what a production system would do; every cache must stay correct
  under the updates the workload makes.
- The data comes from the database at run time: do not embed the fixture, its generator or
  answers precomputed from them in the candidate.
- Keep acknowledged writes durable in each service's MongoDB; do not relax validation or
  authentication (the JWT check of the admin endpoints).
- Do not detect the benchmark (by user agent, request pattern, namespace or environment)
  and behave differently.
- Do not depend on the cluster, host or network beyond the manifest; one candidate image
  runs every application pod.
