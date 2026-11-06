"""train-ticket-k8s check (interim): servicebench's Train Ticket accuracy suite, in the cluster.

    python /task/check.py accuracy --project-root ROOT     (the task's [accuracy] command)

Interim until the task's own correctness check is designed: it deploys the candidate
exactly as the benchmark does (bench.SYSTEM: the same images, manifest and readiness), and
in place of the benchmark's seed runs the servicebench Train Ticket accuracy suite
(`servicebench --mode accuracy`, built from the task image's ORACLE_VIBESYS_COMMIT) in the
seed Job, inside the sample's namespace, against the gateway: the startup catalog, exact
entity schemas, a randomized create/read/update/delete graph, read-your-write, updates and
deletes visible, stale secondary indexes. It runs no load phase. The check passes (exit 0)
when the suite does; otherwise the reason is in the output and the seed pod's log.

The deploy, the seed Job and the teardown are vibesys-evaluator's: this script is a
microservices.main whose benchmark only reports that the suite passed, run in benchmark mode
with its records in the job's artifacts directory.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import bench  # noqa: E402
import dataset as ds  # noqa: E402
import vibesys_evaluator as vse  # noqa: E402
from vibesys_evaluator import microservices as ms  # noqa: E402

WORKLOAD_TOML = Path(__file__).resolve().parent / "accuracy" / "workload.toml"
SUITE_TIMEOUT_SECONDS = 900


def suite_command() -> list[str]:
    """servicebench's accuracy mode against the gateway (every target is the gateway)."""
    return ["servicebench", "--mode", "accuracy", "--workload", str(WORKLOAD_TOML), "--seed", "random"]


def accuracy(_data: ds.Dataset) -> None:
    """Run the suite (in the seed Job, once the deployment serves); raise when it fails."""
    completed = subprocess.run(
        suite_command(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        check=False, timeout=SUITE_TIMEOUT_SECONDS,
    )
    print(completed.stdout, end="", flush=True)
    if completed.returncode != 0:
        raise RuntimeError(
            f"the servicebench Train Ticket accuracy suite failed (exit {completed.returncode}); "
            f"output tail:\n{completed.stdout[-2000:]}"
        )


SYSTEM = dataclasses.replace(bench.SYSTEM, seed=accuracy)
METRICS = {"accuracy_suite_passed": vse.Metric(unit="bool", direction="max")}


def report(run: ms.Run) -> None:
    run.report({"accuracy_suite_passed": 1.0})


def main(argv: list[str]) -> None:
    """`accuracy --project-root ROOT`: one check sample; exit 0 when the suite passed."""
    parser = argparse.ArgumentParser(prog="check.py accuracy")
    parser.add_argument("--project-root", required=True)
    args = parser.parse_args(argv)
    artifacts = os.environ.get("VIBESYS_ARTIFACTS_DIR") or tempfile.mkdtemp(prefix="tt-check-")
    records = Path(artifacts) / "check-records.jsonl"
    command = [sys.executable, str(Path(__file__).resolve()), "benchmark",
               "--project-root", args.project_root, "--vs-output", str(records)]
    os.execv(sys.executable, command)  # the sample's signals and exit status are the check's


if __name__ == "__main__":
    if sys.argv[1:2] == ["accuracy"]:
        main(sys.argv[2:])
    ms.main(
        dataset=ds.generate,
        system=SYSTEM,
        workload=bench.WORKLOAD,
        benchmark=report,
        metrics=METRICS,
        validity=bench.VALIDITY,
    )
