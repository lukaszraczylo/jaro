"""Shared fixtures: pure-Python oracle, vectors and corpus binaries."""

from __future__ import annotations

import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
JARO = HERE.parent.parent
CORPUS = JARO.parent / "corpus"
DATA = HERE / "data"
sys.path.insert(0, str(JARO / "py" / "src"))
sys.path.insert(0, str(HERE))


@pytest.fixture(scope="session")
def vectors() -> dict:
    return json.loads((JARO / "conformance" / "vectors.json").read_text())


@pytest.fixture(scope="session")
def corpus_binaries() -> dict[str, tuple[bytes, object]]:
    """name -> (binary, parsed JSON); binaries come from the Go CLI, else the pure encoder."""
    from jaro.binary import dumps_binary

    DATA.mkdir(exist_ok=True)
    cli = DATA / "jaro-cli"
    if shutil.which("go") and not cli.exists():
        subprocess.run(
            ["go", "build", "-o", str(cli), "./cmd/jaro"], cwd=JARO, check=True
        )
    out = {}
    for src in sorted(CORPUS.glob("*.json")):
        text = src.read_bytes()
        if cli.exists():
            blob = subprocess.run(
                [str(cli), "enc", "-b"], input=text, capture_output=True, check=True
            ).stdout
        else:
            blob = dumps_binary(json.loads(text))
        (DATA / f"{src.stem}.jarob").write_bytes(blob)
        out[src.stem] = (blob, json.loads(text))
    return out
