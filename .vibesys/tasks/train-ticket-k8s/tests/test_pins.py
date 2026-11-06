"""The task pins vibesys-evaluator once per file, and the pins agree.

The Dockerfile installs the PyPI package `vibesys-evaluator` at a version (the ARG
VIBESYS_EVALUATOR_VERSION; the wheel bundles the core, loadgen and otelcapture) on a
digest-pinned python base; the Go module (evaluator/go.mod) requires the Go SDK at the same
release, whose tag spells the version the semver way (v0.3.0-rc.3 is 0.3.0rc3).
"""

from __future__ import annotations

import re
from pathlib import Path

TASK = Path(__file__).resolve().parents[1]
MODULE = "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go"


def dockerfile_version() -> str:
    text = (TASK / "Dockerfile").read_text()
    lines = [line for line in text.splitlines() if not line.startswith("#")]
    code = "\n".join(lines)
    froms = [line.split()[1] for line in lines if line.startswith("FROM ")]
    # Every base is pinned by digest, and none is the evaluator's private image.
    for image in froms:
        assert re.search(r"@sha256:[0-9a-f]{64}$", image), image
        assert "vibesys-evaluator" not in image and "ghcr.io" not in image, image
    versions = re.findall(r"^ARG VIBESYS_EVALUATOR_VERSION=(\S+)$", code, flags=re.M)
    assert len(versions) == 1, versions
    assert re.fullmatch(r"\d+\.\d+\.\d+(?:rc\d+)?", versions[0]), versions[0]
    # The package is installed from PyPI at that version, in the final image, and nothing else
    # installs the evaluator: no build from source, no go install.
    installs = re.findall(r"pip install .*", code)
    assert installs == ['pip install --no-cache-dir "vibesys-evaluator==${VIBESYS_EVALUATOR_VERSION}"'], installs
    assert froms[-1].startswith("python:"), "the task image is the python base plus the evaluator and the task"
    assert "go install" not in text and "archive/" not in text and "git+" not in text
    assert "sdk/vibesys-evaluator" not in code
    return versions[0]


def pep440(tag: str) -> str:
    """The Go SDK's tag as a PyPI version: v0.3.0-rc.3 is 0.3.0rc3."""
    return re.sub(r"-rc\.(\d+)$", r"rc\1", tag.removeprefix("v"))


def gomod_version() -> str:
    text = (TASK / "evaluator" / "go.mod").read_text()
    versions = re.findall(rf"^\s*(?:require\s+)?{re.escape(MODULE)} (\S+)$", text, flags=re.M)
    assert len(versions) == 1, versions
    # A release tag of the Go SDK, not a commit or a pseudo-version.
    assert re.fullmatch(r"v\d+\.\d+\.\d+(-rc\.\d+)?", versions[0]), versions[0]
    assert "replace" not in text, "the Go SDK comes from its published module, not a checkout"
    return versions[0]


def test_the_go_sdk_is_the_pypi_release():
    assert pep440(gomod_version()) == dockerfile_version()


def test_the_go_tag_reads_as_the_pypi_version():
    assert pep440("v0.3.0-rc.3") == "0.3.0rc3"
    assert pep440("v0.3.0") == "0.3.0"


def test_go_sum_holds_the_pinned_module():
    text = (TASK / "evaluator" / "go.sum").read_text()
    assert f"{MODULE} {gomod_version()} h1:" in text
