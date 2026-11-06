"""train-ticket-k8s benchmark: Train Ticket's six core services on Kubernetes, on vibesys-evaluator.

    python /task/bench.py benchmark --project-root ROOT --vs-output PATH   (one sample)

The five parts: (1) the system under test, (2) the initial data (seed),
(3) the workload, (4) the benchmark function, (5) the validity rules. The
data is dataset.py's fixture, created through the public API by the Go seed
(evaluator/cmd/seed). The workload (operations, request distributions, the
model the updates change and the checks of the responses) is the Go generator
of evaluator/cmd/generator; this script only orchestrates.
"""

from __future__ import annotations

import dataclasses
import json
import statistics
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import dataset as ds  # noqa: E402
import vibesys_evaluator as vse  # noqa: E402
from vibesys_evaluator import microservices as ms  # noqa: E402
from vibesys_evaluator.microservices import k8s  # noqa: E402

GATEWAY = "http://ts-gateway:8080"
SERVICES = ("config", "station", "train", "travel", "route", "price")

# ---------------------------------------------------------------- (2) seed

SEED_TIMEOUT_SECONDS = 600


def seed_command(dataset_file: str) -> list[str]:
    """The Go seed (on the task image's PATH): creates the fixture through the gateway."""
    return ["tt-seed", "--dataset", dataset_file, "--target", GATEWAY, "--timeout", f"{SEED_TIMEOUT_SECONDS}s"]


def seed_summary(output: str, data: ds.Dataset) -> dict:
    """The seed's summary (its last JSON line), checked to be of this dataset."""
    summary = None
    for line in output.splitlines():
        if line.startswith("{"):
            try:
                summary = json.loads(line)
            except json.JSONDecodeError:
                pass
    if summary is None:
        raise RuntimeError(f"seed wrote no summary; output tail:\n{output[-1500:]}")
    if summary.get("dataset_digest") != data.digest:
        raise RuntimeError(
            f"seed created the dataset {summary.get('dataset_digest')}, want {data.digest} "
            "(dataset.py and internal/ttdata disagree)"
        )
    return summary


def seed(data: ds.Dataset) -> None:
    """Create the fixture (runs in the seed Job, once the deployment serves).

    Every record's config, stations, train, route, price and trip are created through the
    gateway with the services' own API, then read back and compared; the startup catalog
    the services write themselves stays as it is.
    """
    with tempfile.NamedTemporaryFile("w", suffix=".json") as file:
        json.dump(dataclasses.asdict(data), file)
        file.flush()
        completed = subprocess.run(
            seed_command(file.name), stdout=subprocess.PIPE, text=True, check=False,
            timeout=SEED_TIMEOUT_SECONDS + 60,
        )
    print(completed.stdout, end="", flush=True)
    if completed.returncode != 0:
        raise RuntimeError(f"seed exited {completed.returncode}; output tail:\n{completed.stdout[-1500:]}")
    print(f"seed: {json.dumps(seed_summary(completed.stdout, data))}", flush=True)


# -------------------------------------------------- (1) system under test

SYSTEM = k8s.App(
    name="train-ticket-k8s",
    manifest="k8s/manifest.yaml",  # ${NAMESPACE} and ${TRAIN_TICKET_IMAGE} are substituted
    # The candidate's one image, built from PROJECT_ROOT/Dockerfile with the repository root
    # as its context (the Maven reactor); every service's Deployment runs it with its own
    # command. k8s.App cannot build one image per service from a shared Dockerfile with a
    # build argument (see README.md).
    images={"TRAIN_TICKET_IMAGE": "."},
    gateway=GATEWAY,
    # Every service answers its collection through the gateway (MongoDB included).
    ready=[ms.get(f"/api/v1/{service}service/{collection}") for service, collection in (
        ("config", "configs"), ("station", "stations"), ("train", "trains"),
        ("travel", "trips"), ("route", "routes"), ("price", "prices"),
    )],
    seed=seed,
    restartable=[f"ts-{service}-service" for service in SERVICES],
)


# ------------------------------------------------------------ (3) workload
# The operations (35% lists, 25% point and secondary-index reads, 35% updates each followed
# by a read-your-write read and, where a secondary key moved, a read by the retired key),
# how each request is drawn, the model the updates change and the checks are Go:
# evaluator/internal/ttworkload on the vibesys-evaluator Go SDK. The task image builds it as
# tt-generator (evaluator/cmd/generator); each phase pod runs it under loadgen to generate
# the request stream, then to check the phase's responses.

WORKLOAD = ms.Generator(["tt-generator"])  # on the task image's PATH


# ------------------------------------------------------------ (4) benchmark

CLIENTS = 128
CLOSED_TRIALS, CLOSED_SECS, CLOSED_WARMUP = 3, 30, 10
# The first trial warms the JVMs up (JIT compilation, connection pools) for longer. On the
# cluster, with 2 CPUs per service, the route service's throughput was still climbing after
# 150 s of load (3400 -> 4000 -> 5100 ops/s over three trials after a 30 s warmup), so the
# first window starts after 180 s.
FIRST_WARMUP = 180
OPEN_TRIALS, OPEN_SECS, OPEN_WARMUP = 3, 30, 10
OPEN_LOOP_LOAD_FRACTION = 0.7


def benchmark(run: ms.Run) -> None:
    """Capacity in a closed loop, then the p99 latency at 70% of it in an open loop.

    Three closed-loop trials (128 clients, 30 s after a warmup of 180 s for the
    first and 10 s for the others): the throughput is their median. Three
    open-loop trials (10 s warmup, 30 s, evenly spaced arrivals) at 0.7 x that
    throughput: the p99 is their median. The sample's updates carry from phase
    to phase, so nothing is reset.
    """
    closed = [
        run.closed_loop(clients=CLIENTS, secs=CLOSED_SECS, warmup=FIRST_WARMUP if trial == 0 else CLOSED_WARMUP).ops_per_sec
        for trial in range(CLOSED_TRIALS)
    ]
    throughput = statistics.median(closed)
    # p99 is measured at a fixed fraction of this sample's own throughput, so
    # a faster candidate is held to proportionally more load.
    rate = OPEN_LOOP_LOAD_FRACTION * throughput
    p99 = statistics.median(
        run.open_loop(rate=rate, secs=OPEN_SECS, warmup=OPEN_WARMUP, arrivals="uniform").p99_ms
        for _ in range(OPEN_TRIALS)
    )
    run.report({"operations_per_second": throughput, "latency_ms.p99": p99})


# ------------------------------------------------------------- (5) validity

METRICS = {
    "operations_per_second": vse.Metric(unit="ops/s", direction="max"),
    "latency_ms.p99": vse.Metric(unit="ms", direction="min"),
}
# No error and no failed check in any window, every operation of the mix completed,
# and the open loop offers at least 95% of its rate.
VALIDITY = vse.Validity(max_error_rate=0.0, min_sustained_ratio=0.95, min_ops_per_operation=1)


if __name__ == "__main__":
    ms.main(
        dataset=ds.generate,
        system=SYSTEM,
        workload=WORKLOAD,
        benchmark=benchmark,
        metrics=METRICS,
        validity=VALIDITY,
    )
