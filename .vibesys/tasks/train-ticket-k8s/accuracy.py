"""train-ticket-k8s accuracy check: the candidate's answers against Train Ticket's semantics.

    python /task/accuracy.py accuracy --project-root ROOT   (the task's [accuracy] command)

vibesys-evaluator's accuracy run (spec.md, "Accuracy run"): it deploys and seeds the candidate
exactly as the benchmark does (bench.SYSTEM: the same images, manifest, readiness and seed),
runs PHASES open-loop phases of the accuracy workload at a fixed, low RATE with every request
and response captured, restarts each of the six services once between them (FAULTS), tears the
application down, and runs the oracle on the captured history. The exit status is the
verdict's: 0 PASS, 1 FAIL, 2 UNKNOWN (also when the run could not complete), with the report,
the failures' timelines and the history in /vibesys/artifacts/accuracy/.

- The workload is Go: evaluator/internal/ttaccuracy, built as tt-accuracy. One request per
  operation, each with the info the oracle reads back: creates, updates, deletes and upserts,
  reads by key, lists, secondary-key queries and the trip joins, over the services' startup
  catalog, the seeded fixture and a few ids of the check's own per service. It is not the
  benchmark's workload (bench.WORKLOAD), which is why the check is its own script: an accuracy
  run's phase pods run this script's `phase` command, and so this script's workload.
- The oracle is Go: evaluator/cmd/oracle (tt-oracle) on the SDK's go/check, the model in
  evaluator/internal/ttoracle (README.md, "The accuracy check").
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import bench  # noqa: E402
import dataset as ds  # noqa: E402
from vibesys_evaluator import microservices as ms  # noqa: E402

WORKLOAD = ms.Generator(["tt-accuracy"])  # on the task image's PATH
ORACLE = ["tt-oracle"]  # on the task image's PATH

# The load: fixed, so the history and the check's cost do not depend on the candidate's speed,
# and low, so that responses are judged rather than capacity: at 40 operations per second the
# base answers in milliseconds, loses none, and the most operations in flight on one key stay
# far below the checker's limit of about 12 (README.md, "The accuracy check").
PHASES, RATE, SECS, WARMUP = 4, 40, 60, 5

# The faults: each service restarted once, at a quiescent point between phases. A restart
# re-applies the service's startup catalog (an overwrite for routes and prices, create-if-absent
# for the others), which the oracle models; anything else it must keep: every acknowledged
# write survives. Routes and prices first (their catalog overwrites what phase 0 changed),
# trips last (their joins read routes and trains).
FAULTS = [
    ms.Fault.restart("ts-route-service", after=0),
    ms.Fault.restart("ts-price-service", after=0),
    ms.Fault.restart("ts-station-service", after=1),
    ms.Fault.restart("ts-config-service", after=1),
    ms.Fault.restart("ts-train-service", after=2),
    ms.Fault.restart("ts-travel-service", after=2),
]

ACCURACY = ms.Accuracy(
    phases=PHASES, rate=RATE, secs=SECS, warmup=WARMUP, arrivals="poisson", faults=FAULTS, oracle=ORACLE
)


def no_benchmark(_run: ms.Run) -> None:
    """This script is the accuracy check; bench.py is the benchmark."""
    msg = "accuracy.py only runs the accuracy check (`accuracy.py accuracy`); bench.py is the benchmark"
    raise RuntimeError(msg)


if __name__ == "__main__":
    ms.main(
        dataset=ds.generate,
        system=bench.SYSTEM,
        workload=WORKLOAD,
        benchmark=no_benchmark,
        metrics=bench.METRICS,
        validity=bench.VALIDITY,
        accuracy=ACCURACY,
    )
