"""The task pins vibesys-evaluator once per file, and the pins agree.

The Dockerfile installs the Python package from PyPI at the release VIBESYS_EVALUATOR_VERSION
(its platform wheel bundles loadgen, otelcapture and the core's vibesys-evaluator binary); the
Go module (evaluator/go.mod) requires the Go SDK at that release.
"""

from __future__ import annotations

import re
from pathlib import Path

TASK = Path(__file__).resolve().parents[1]
MODULE = "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go"
ARG = "VIBESYS_EVALUATOR_VERSION"


def dockerfile_version() -> str:
    text = (TASK / "Dockerfile").read_text()
    pins = re.findall(rf"^ARG {ARG}=(\S+)$", text, flags=re.M)
    assert len(pins) == 1, f"the Dockerfile pins {ARG} once, before the first FROM"
    # A release tag of the Go SDK, not a commit or a pseudo-version.
    assert re.fullmatch(r"v\d+\.\d+\.\d+(-rc\.\d+)?", pins[0]), pins[0]
    # The one install of the evaluator names the ARG, never a literal version, a source URL or
    # a copy; the wheel brings the binaries, so nothing builds or installs them from Go.
    lines = [line for line in text.splitlines() if not line.startswith("#")]
    pip = [line for line in lines if "pip install" in line]
    assert pip == [f'RUN pip install --no-cache-dir "vibesys-evaluator==${{{ARG}#v}}"'], pip
    assert "go install" not in text and "archive/" not in text and "git+" not in text
    assert "sdk/vibesys-evaluator" not in "\n".join(lines)
    return pins[0]


def pep440(tag: str) -> str:
    """What pip reads `${VERSION#v}` as: v0.3.0-rc.1 is 0.3.0rc1."""
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
    assert pep440("v0.3.0-rc.1") == "0.3.0rc1"
    assert pep440("v0.3.0") == "0.3.0"
    assert re.fullmatch(r"\d+\.\d+\.\d+(rc\d+)?", pep440(dockerfile_version()))


def test_go_sum_holds_the_pinned_module():
    text = (TASK / "evaluator" / "go.sum").read_text()
    assert f"{MODULE} {gomod_version()} h1:" in text
