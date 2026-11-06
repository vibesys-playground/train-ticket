"""The task pins vibesys-evaluator once per file, and the pins agree.

The Dockerfile installs the Python package, loadgen and otelcapture at VIBESYS_COMMIT; the
Go module (evaluator/go.mod) requires the Go SDK (the loadgen module) at that commit's
pseudo-version. A release replaces both.
"""

from __future__ import annotations

import re
from pathlib import Path

TASK = Path(__file__).resolve().parents[1]
MODULE = "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen"


def dockerfile_commit() -> str:
    text = (TASK / "Dockerfile").read_text()
    pins = re.findall(r"^ARG VIBESYS_COMMIT=([0-9a-f]{40})$", text, flags=re.M)
    assert len(pins) == 1, "the Dockerfile pins VIBESYS_COMMIT once, before the first FROM"
    # Every use of the evaluator's sources names the ARG, never a literal commit.
    for line in text.splitlines():
        if "uw-syfi/vibesys" in line and "@" in line and "sdk/vibesys-evaluator" in line:
            assert "${VIBESYS_COMMIT}" in line, line
    return pins[0]


def gomod_version() -> str:
    text = (TASK / "evaluator" / "go.mod").read_text()
    versions = re.findall(rf"^\s*(?:require\s+)?{re.escape(MODULE)} (\S+)$", text, flags=re.M)
    assert len(versions) == 1, versions
    assert "replace" not in text, "the Go SDK comes from its published module, not a checkout"
    return versions[0]


def test_the_go_sdk_is_the_dockerfiles_commit():
    commit, version = dockerfile_commit(), gomod_version()
    # A pseudo-version ends in the commit's first 12 hex digits.
    assert re.fullmatch(r"v\d+\.\d+\.\d+-\S+\.\d{14}-[0-9a-f]{12}", version), version
    assert version.endswith("-" + commit[:12]), f"go.mod {version} is not VIBESYS_COMMIT {commit}"


def test_go_sum_holds_the_pinned_module():
    text = (TASK / "evaluator" / "go.sum").read_text()
    assert f"{MODULE} {gomod_version()} h1:" in text
