"""The task pins vibesys-evaluator once per file, and the pins agree.

The Dockerfile installs the Python package (PyPI), loadgen and otelcapture (go install) at
the release VIBESYS_EVALUATOR_VERSION; the Go module (evaluator/go.mod) requires the Go SDK
(the loadgen module) at that release.
"""

from __future__ import annotations

import re
from pathlib import Path

TASK = Path(__file__).resolve().parents[1]
MODULE = "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen"
ARG = "VIBESYS_EVALUATOR_VERSION"


def dockerfile_version() -> str:
    text = (TASK / "Dockerfile").read_text()
    pins = re.findall(rf"^ARG {ARG}=(\S+)$", text, flags=re.M)
    assert len(pins) == 1, f"the Dockerfile pins {ARG} once, before the first FROM"
    # A release tag of the Go SDK, not a commit or a pseudo-version.
    assert re.fullmatch(r"v\d+\.\d+\.\d+(-rc\.\d+)?", pins[0]), pins[0]
    # Every install of the evaluator names the ARG, never a literal version or a source URL.
    installs = [line for line in text.splitlines() if "vibesys-evaluator" in line and not line.startswith("#")]
    go = [line for line in installs if MODULE in line]
    pip = [line for line in installs if "pip install" in line]
    assert len(go) == 2 and all(f"@${{{ARG}}}" in line for line in go), go
    assert pip == [f'RUN pip install --no-cache-dir "vibesys-evaluator==${{{ARG}#v}}"'], pip
    assert "archive/" not in text and "git+" not in text
    return pins[0]


def pep440(tag: str) -> str:
    """What pip reads `${VERSION#v}` as: v0.2.0-rc.1 is 0.2.0rc1."""
    return re.sub(r"-rc\.(\d+)$", r"rc\1", tag.removeprefix("v"))


def gomod_version() -> str:
    text = (TASK / "evaluator" / "go.mod").read_text()
    versions = re.findall(rf"^\s*(?:require\s+)?{re.escape(MODULE)} (\S+)$", text, flags=re.M)
    assert len(versions) == 1, versions
    assert "replace" not in text, "the Go SDK comes from its published module, not a checkout"
    return versions[0]


def test_the_go_sdk_is_the_dockerfiles_release():
    assert gomod_version() == dockerfile_version()


def test_pip_reads_the_release_as_its_pypi_version():
    assert pep440("v0.2.0-rc.1") == "0.2.0rc1"
    assert pep440("v0.2.0") == "0.2.0"
    assert re.fullmatch(r"\d+\.\d+\.\d+(rc\d+)?", pep440(dockerfile_version()))


def test_go_sum_holds_the_pinned_module():
    text = (TASK / "evaluator" / "go.sum").read_text()
    assert f"{MODULE} {gomod_version()} h1:" in text
