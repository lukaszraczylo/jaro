from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from conftest import same

import jaro

VECTORS = json.loads(
    (Path(__file__).parent / ".." / ".." / "conformance" / "vectors.json").read_text(
        "utf-8"
    )
)

# Vector names whose bytes differ from the Go reference, with the reason.
TEXT_PARITY_ALLOWLIST: dict[str, str] = {}
BINARY_PARITY_ALLOWLIST: dict[str, str] = {}
BINARY_DEDUP_ALLOWLIST: dict[str, str] = {}

TREES = {t["name"]: t["json"] for t in VECTORS["trees"]}
# The generated vectors come first; the hand-written decode-only case "table" reuses a generated name.
TEXT: dict[str, dict[str, Any]] = {}
for _case in VECTORS["text"]:
    TEXT.setdefault(_case["name"], _case)


def _ids(items: list[dict[str, Any]]) -> list[str]:
    return [i["name"] for i in items]


def test_vector_counts() -> None:
    assert VECTORS["version"] == 1
    assert (
        len(VECTORS["text"]) >= 30
        and len(VECTORS["binary"]) >= 30
        and len(VECTORS["trees"]) >= 15
    )


@pytest.mark.parametrize("case", VECTORS["text"], ids=_ids(VECTORS["text"]))
def test_text_decodes(case: dict[str, Any]) -> None:
    got = jaro.loads(case["jaro"])
    assert same(got, case["json"]), (got, case["json"])
    assert same(jaro.loads(case["jaro"].encode("utf-8")), case["json"])


@pytest.mark.parametrize("case", VECTORS["binary"], ids=_ids(VECTORS["binary"]))
def test_binary_decodes(case: dict[str, Any]) -> None:
    got = jaro.loads_binary(bytes.fromhex(case["hex"]))
    assert same(got, case["json"]), (got, case["json"])


@pytest.mark.parametrize("src", VECTORS["badText"], ids=lambda s: repr(s)[:30])
def test_bad_text_rejected(src: str) -> None:
    with pytest.raises(jaro.JaroError):
        jaro.loads(src)


@pytest.mark.parametrize("hexed", VECTORS["badTextHex"])
def test_bad_text_bytes_rejected(hexed: str) -> None:
    with pytest.raises(jaro.JaroError):
        jaro.loads(bytes.fromhex(hexed))


@pytest.mark.parametrize("hexed", VECTORS["badBinaryHex"], ids=lambda s: s or "empty")
def test_bad_binary_rejected(hexed: str) -> None:
    with pytest.raises(jaro.JaroError):
        jaro.loads_binary(bytes.fromhex(hexed))


@pytest.mark.parametrize("name", sorted(TREES))
def test_tree_round_trips(name: str) -> None:
    tree = TREES[name]
    assert same(jaro.loads(jaro.dumps(tree)), tree)
    assert same(jaro.loads(jaro.dumps(tree, dedup=True)), tree)
    assert same(jaro.loads_binary(jaro.dumps_binary(tree)), tree)
    assert same(jaro.loads_binary(jaro.dumps_binary(tree, dedup=True)), tree)


PARITY_TEXT = sorted(n for n in TEXT if n in TREES)
PARITY_BIN = sorted(c["name"] for c in VECTORS["binary"] if c["name"] in TREES)
PARITY_BIN_DEDUP = sorted(
    c["name"]
    for c in VECTORS["binary"]
    if c["name"].endswith("-dedup") and c["name"][: -len("-dedup")] in TREES
)


@pytest.mark.parametrize("name", PARITY_TEXT)
def test_text_parity(name: str) -> None:
    if name in TEXT_PARITY_ALLOWLIST:
        pytest.skip(TEXT_PARITY_ALLOWLIST[name])
    assert jaro.dumps(TREES[name]) == TEXT[name]["jaro"]


@pytest.mark.parametrize("name", PARITY_BIN)
def test_binary_parity(name: str) -> None:
    if name in BINARY_PARITY_ALLOWLIST:
        pytest.skip(BINARY_PARITY_ALLOWLIST[name])
    want = next(c["hex"] for c in VECTORS["binary"] if c["name"] == name)
    assert jaro.dumps_binary(TREES[name]).hex() == want


@pytest.mark.parametrize("name", PARITY_BIN_DEDUP)
def test_binary_dedup_parity(name: str) -> None:
    if name in BINARY_DEDUP_ALLOWLIST:
        pytest.skip(BINARY_DEDUP_ALLOWLIST[name])
    want = next(c["hex"] for c in VECTORS["binary"] if c["name"] == name)
    assert jaro.dumps_binary(TREES[name[: -len("-dedup")]], dedup=True).hex() == want
