"""The task pins vibesys-evaluator once per file, and the pins agree.

The Dockerfile builds FROM vibesys-evaluator's microservices image at a release, by tag and
digest (the image holds the PyPI package of that version, its bundled binaries, kubectl and
buildctl); the Go module (evaluator/go.mod) requires the Go SDK at the same release, whose tag
spells the version the semver way (v0.3.0-rc.2 is 0.3.0rc2).
"""

from __future__ import annotations

import re
from pathlib import Path

TASK = Path(__file__).resolve().parents[1]
MODULE = "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/go"
IMAGE = "ghcr.io/uw-syfi/vibesys-evaluator-microservices"


def dockerfile_version() -> str:
    text = (TASK / "Dockerfile").read_text()
    lines = [line for line in text.splitlines() if not line.startswith("#")]
    froms = [line.split()[1] for line in lines if line.startswith("FROM ")]
    evaluator = [image for image in froms if image.startswith(IMAGE + ":")]
    assert len(evaluator) == 1, f"the Dockerfile builds FROM {IMAGE} once: {froms}"
    # A release's tag and the digest it was published with, never a floating tag.
    match = re.fullmatch(rf"{re.escape(IMAGE)}:(\d+\.\d+\.\d+(?:rc\d+)?)@sha256:[0-9a-f]{{64}}", evaluator[0])
    assert match, evaluator[0]
    assert froms[-1] == evaluator[0], "the task image is the evaluator's image plus the task"
    # Every other base is pinned by digest too.
    for image in froms:
        assert re.search(r"@sha256:[0-9a-f]{64}$", image), image
    # Nothing installs the evaluator besides the image: no pip install, no build from source.
    assert "pip install" not in "\n".join(lines)
    assert "go install" not in text and "archive/" not in text and "git+" not in text
    assert "sdk/vibesys-evaluator" not in "\n".join(lines)
    return match.group(1)


def pep440(tag: str) -> str:
    """The Go SDK's tag as a PyPI version: v0.3.0-rc.2 is 0.3.0rc2."""
    return re.sub(r"-rc\.(\d+)$", r"rc\1", tag.removeprefix("v"))


def gomod_version() -> str:
    text = (TASK / "evaluator" / "go.mod").read_text()
    versions = re.findall(rf"^\s*(?:require\s+)?{re.escape(MODULE)} (\S+)$", text, flags=re.M)
    assert len(versions) == 1, versions
    # A release tag of the Go SDK, not a commit or a pseudo-version.
    assert re.fullmatch(r"v\d+\.\d+\.\d+(-rc\.\d+)?", versions[0]), versions[0]
    assert "replace" not in text, "the Go SDK comes from its published module, not a checkout"
    return versions[0]


def test_the_go_sdk_is_the_images_release():
    assert pep440(gomod_version()) == dockerfile_version()


def test_the_go_tag_reads_as_the_pypi_version():
    assert pep440("v0.3.0-rc.2") == "0.3.0rc2"
    assert pep440("v0.3.0") == "0.3.0"


def test_go_sum_holds_the_pinned_module():
    text = (TASK / "evaluator" / "go.sum").read_text()
    assert f"{MODULE} {gomod_version()} h1:" in text
