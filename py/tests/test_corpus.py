from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
from conftest import same

import jaro

CORPUS = Path(
    os.environ.get("JARO_CORPUS")
    or Path(__file__).parent / ".." / ".." / ".." / "corpus"
)
FILES = sorted(CORPUS.glob("*.json")) if CORPUS.is_dir() else []


@pytest.mark.skipif(not FILES, reason="no corpus directory")
@pytest.mark.parametrize("path", FILES, ids=lambda p: p.name)
def test_real_files_round_trip(path: Path) -> None:
    tree = json.loads(path.read_bytes())
    for dedup in (False, True):
        assert same(jaro.loads(jaro.dumps(tree, dedup=dedup)), tree)
        assert same(jaro.loads_binary(jaro.dumps_binary(tree, dedup=dedup)), tree)
