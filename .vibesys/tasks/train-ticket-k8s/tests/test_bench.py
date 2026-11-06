"""bench.py and accuracy.py build their objects, and the manifest is one the package deploys.

Run with vibesys-evaluator installed at the task's pin (README.md, "Tests"):

    python -m pytest .vibesys/tasks/train-ticket-k8s/tests
"""

from __future__ import annotations

import dataclasses
import importlib
import json
import re
import subprocess
import sys
import tomllib
from pathlib import Path

import pytest
import yaml

TASK = Path(__file__).resolve().parents[1]
REPOSITORY = TASK.parents[2]
sys.path.insert(0, str(TASK))

import bench  # noqa: E402
import dataset as ds  # noqa: E402
from vibesys_evaluator import microservices as ms  # noqa: E402
from vibesys_evaluator.microservices import k8s  # noqa: E402

SERVICES = [f"ts-{name}-service" for name in bench.SERVICES]


def rendered() -> list[dict]:
    # The manifest as the deploy renders it: its variables substituted, every object in the
    # sample's namespace. (The renderer itself is the SDK's Go k8s.Render, which
    # evaluator/manifest/manifest_test.go runs on this manifest.)
    text = (TASK / bench.SYSTEM.manifest).read_text().replace("${NAMESPACE}", "vse-test")
    for variable in bench.SYSTEM.images:
        text = text.replace("${" + variable + "}", f"registry/vse/image:{variable.lower()}")
    assert "${" not in "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
    objects = [o for o in yaml.safe_load_all(text) if o]
    for o in objects:
        o["metadata"]["namespace"] = "vse-test"
    return objects


def test_the_system_is_a_k8s_app_of_the_candidates_one_image():
    app = bench.SYSTEM
    assert isinstance(app, k8s.App)
    assert app.name == "train-ticket-k8s"
    assert dict(app.images) == {"TRAIN_TICKET_IMAGE": "."}
    assert (REPOSITORY / "Dockerfile").is_file(), "the candidate's image is built from the repository root"
    assert app.gateway == "http://ts-gateway:8080"
    assert len(app.ready) == 6 and app.seed is bench.seed and app.data_tier == ""
    assert list(app.restartable) == SERVICES


def test_the_manifest_renders_and_fixes_resources():
    objects = rendered()
    deployments = {o["metadata"]["name"]: o for o in objects if o["kind"] == "Deployment"}
    services = {o["metadata"]["name"]: o for o in objects if o["kind"] == "Service"}
    assert set(SERVICES) <= set(deployments) and "ts-gateway" in services
    assert set(bench.SYSTEM.restartable) <= set(deployments)
    for name, deployment in deployments.items():
        assert deployment["metadata"]["namespace"] == "vse-test"
        for container in deployment["spec"]["template"]["spec"]["containers"]:
            resources = container["resources"]
            assert resources["requests"] == resources["limits"], name
            assert "readinessProbe" in container, name
            assert "@sha256:" in container["image"] or container["image"].startswith("registry/"), name
        assert name in services, f"{name} has no Service"
    for name in SERVICES:
        container = deployments[name]["spec"]["template"]["spec"]["containers"][0]
        assert container["image"] == "registry/vse/image:train_ticket_image"
        assert container["command"] == [name]
        port = container["ports"][0]["containerPort"]
        assert services[name]["spec"]["ports"][0]["port"] == port
        mongo = name.replace("-service", "-mongo")
        assert mongo in deployments and services[mongo]["spec"]["ports"][0]["port"] == 27017


def test_the_gateway_routes_every_service():
    config = next(o for o in rendered() if o["kind"] == "ConfigMap")["data"]["nginx.conf"]
    services = {o["metadata"]["name"]: o for o in rendered() if o["kind"] == "Service"}
    for name in bench.SERVICES:
        port = services[f"ts-{name}-service"]["spec"]["ports"][0]["port"]
        assert f"server ts-{name}-service:{port};" in config
        assert re.search(rf"location /api/v1/{name}service \{{\s*proxy_pass http://{name};", config)


def test_the_candidate_dockerfile_provides_every_command():
    text = (REPOSITORY / "Dockerfile").read_text()
    listed = re.search(r'ENV SERVICES="([^"]+)"', text).group(1).split()
    assert listed == SERVICES


def test_the_workload_is_the_go_generator():
    assert isinstance(bench.WORKLOAD, ms.Generator)
    assert list(bench.WORKLOAD.command) == ["tt-generator"]


class FakeRun:
    def __init__(self, throughputs, p99s):
        self.throughputs, self.p99s = list(throughputs), list(p99s)
        self.calls, self.reported = [], None

    def closed_loop(self, *, clients, secs, warmup):
        self.calls.append(("closed", clients, secs, warmup))
        return ms.PhaseResult(ops_per_sec=self.throughputs.pop(0), p50_ms=1.0, p99_ms=50.0)

    def open_loop(self, *, rate, secs, warmup, arrivals="poisson"):
        self.calls.append(("open", rate, secs, warmup, arrivals))
        return ms.PhaseResult(ops_per_sec=rate, p50_ms=1.0, p99_ms=self.p99s.pop(0))

    def report(self, values):
        self.reported = dict(values)


def test_benchmark_reports_median_capacity_and_p99_at_seventy_percent():
    run = FakeRun([900.0, 1100.0, 1000.0], [12.0, 30.0, 15.0])
    bench.benchmark(run)
    assert run.calls[:3] == [("closed", 128, 30, 180), ("closed", 128, 30, 10), ("closed", 128, 30, 10)]
    assert run.calls[3:] == [("open", pytest.approx(700.0), 30, 10, "uniform")] * 3
    assert run.reported == {"operations_per_second": 1000.0, "latency_ms.p99": 15.0}
    assert set(run.reported) == set(bench.METRICS)
    assert bench.VALIDITY.max_error_rate == 0.0 and bench.VALIDITY.min_ops_per_operation == 1


def test_the_dataset_file_is_what_the_go_side_reads():
    from vibesys_evaluator.microservices._private import dataset as dataset_file

    encoded = json.loads(dataset_file.encode(ds.generate()))
    assert encoded == {"seed": ds.SEED, "records": ds.RECORDS, "digest": ds.DIGEST}


def test_seed_runs_the_go_seed_and_checks_its_digest(monkeypatch):
    seen = {}

    def fake_run(argv, **_):
        seen["argv"] = argv
        seen["file"] = json.loads(Path(argv[argv.index("--dataset") + 1]).read_text())
        summary = {"dataset_digest": ds.DIGEST, "records": ds.RECORDS}
        return subprocess.CompletedProcess(argv, 0, stdout="log\n" + json.dumps(summary) + "\n")

    monkeypatch.setattr(bench.subprocess, "run", fake_run)
    bench.seed(ds.generate())
    assert seen["argv"][0] == "tt-seed" and seen["argv"][seen["argv"].index("--target") + 1] == bench.GATEWAY
    assert seen["file"] == dataclasses.asdict(ds.generate())
    wrong = ds.Dataset(ds.SEED, ds.RECORDS, "0" * 64)
    with pytest.raises(RuntimeError, match="disagree"):
        bench.seed(wrong)


def test_the_accuracy_check_runs_its_workload_and_oracle_at_a_fixed_low_load():
    accuracy = importlib.import_module("accuracy")
    assert isinstance(accuracy.WORKLOAD, ms.Generator)
    assert list(accuracy.WORKLOAD.command) == ["tt-accuracy"]
    check = accuracy.ACCURACY
    assert isinstance(check, ms.Accuracy)
    assert list(check.oracle) == ["tt-oracle"]
    assert (check.phases, check.rate, check.secs, check.warmup) == (4, 40, 60, 5)
    # every service restarted once, between phases, each a Deployment the App lets it restart
    assert sorted(f.deployment for f in check.faults) == sorted(SERVICES)
    assert all(f.action == "restart" and 0 <= f.after < check.phases - 1 for f in check.faults)
    assert set(f.deployment for f in check.faults) <= set(bench.SYSTEM.restartable)
    with pytest.raises(RuntimeError, match="bench.py is the benchmark"):
        accuracy.no_benchmark(None)


def test_the_oracle_models_the_faults_the_check_runs():
    # the oracle maps a restart's Deployment to its table (ttoracle.ServiceTable): ts-<table>-service
    accuracy = importlib.import_module("accuracy")
    tables = {"config", "station", "train", "route", "price", "travel"}
    for fault in accuracy.ACCURACY.faults:
        assert re.fullmatch(r"ts-(\w+)-service", fault.deployment).group(1) in tables


def test_the_task_manifest_is_schema_0_2_without_a_candidate_server():
    manifest = tomllib.loads((TASK / "vibesys.input.toml").read_text())
    assert manifest["version"] == "0.2"
    assert "candidate" not in manifest and "evaluator" not in manifest
    assert manifest["environment"]["needs"] == ["kubernetes"]
    assert manifest["benchmark"]["command"][:3] == ["python", "/task/bench.py", "benchmark"]
    assert manifest["benchmark"]["result_protocol"] == 2
    assert manifest["accuracy"]["command"] == [
        "python", "/task/accuracy.py", "accuracy", "--project-root", "${PROJECT_ROOT}"
    ]
    objectives = tomllib.loads((TASK / "objectives.toml").read_text())
    assert [o["name"] for o in objectives["objective"]] == list(bench.METRICS)
    for objective in objectives["objective"]:
        assert objective["direction"] == bench.METRICS[objective["name"]].direction


def test_manifest_objects_parse_without_the_package():
    # A plain YAML reader sees the same documents (no templating beyond ${...}).
    documents = [d for d in yaml.safe_load_all((TASK / bench.SYSTEM.manifest).read_text()) if d]
    assert len(documents) == len(rendered())
