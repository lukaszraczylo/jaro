from __future__ import annotations

import random
from typing import Any

from conftest import same

import jaro

SEED = 20261011
TREES = 400
WORDS = [
    "red",
    "green",
    "blue",
    "x y",
    "^",
    "",
    "a:b",
    "&a",
    "*a",
    "-",
    ".5",
    "1e3",
    "é",
    "😀",
    "a\nb",
    'q"q',
    "#h",
    "~",
    "true",
]


def scalar(r: random.Random) -> Any:
    c = r.random()
    if c < 0.1:
        return None
    if c < 0.2:
        return r.random() < 0.5
    if c < 0.45:
        return r.choice(
            [
                0,
                1,
                -1,
                127,
                128,
                2**53 - 1,
                2**53 + 1,
                -(2**63),
                2**63 - 1,
                r.randint(-1000, 1000),
            ]
        )
    if c < 0.65:
        return r.choice(
            [
                round(r.uniform(-1000, 1000), r.randint(0, 6)),
                r.uniform(-1, 1) * 10 ** r.randint(-12, 25),
                0.1,
                1e21,
                5e-324,
                2.0**53,
            ]
        )
    return r.choice(WORDS)


def table(r: random.Random, depth: int) -> list[Any]:
    keys = [f"k{i}" for i in range(r.randint(1, 5))]
    miss = r.choice([0.0, 0.0, 0.1, 0.3, 0.6])
    kinds = {
        k: r.choice(["int", "small", "float", "str", "bool", "obj", "any"])
        for k in keys
    }
    rows: list[Any] = []
    for i in range(r.randint(1, 8)):
        row: dict[str, Any] = {}
        for k in keys:
            if r.random() < miss:
                continue
            kind = kinds[k]
            if kind == "int":
                row[k] = r.randint(0, 100) + i * 3
            elif kind == "small":
                row[k] = i
            elif kind == "float":
                row[k] = round(r.uniform(0, 1000), 2)
            elif kind == "str":
                row[k] = r.choice(WORDS[:6])
            elif kind == "bool":
                row[k] = r.random() < 0.5
            elif kind == "obj":
                row[k] = {
                    s: scalar(r)
                    for s in ("u", "v", "w")[: r.randint(1, 3)]
                    if r.random() < 0.85
                }
            else:
                row[k] = tree(r, depth + 1)
        rows.append(row)
    return rows


def tree(r: random.Random, depth: int = 0) -> Any:
    c = r.random()
    if depth > 4 or c < 0.3:
        return scalar(r)
    if c < 0.5:
        return [tree(r, depth + 1) for _ in range(r.randint(0, 5))]
    if c < 0.65:
        return {
            f"{r.choice(WORDS)}{i}": tree(r, depth + 1) for i in range(r.randint(0, 4))
        }
    if c < 0.85:
        return table(r, depth)
    if c < 0.93:
        return {
            f"row{i}": {"a": scalar(r), "b": scalar(r), "c": {"x": 1, "y": 2}}
            for i in range(r.randint(0, 5))
        }
    sub = {"name": "repeated sub-tree", "tags": ["a", "b", "c"], "n": 12345}
    return [sub, sub, tree(r, depth + 1), sub]


def test_random_trees_round_trip() -> None:
    r = random.Random(SEED)
    for _ in range(TREES):
        t = tree(r)
        for dedup in (False, True):
            text = jaro.dumps(t, dedup=dedup)
            assert same(jaro.loads(text), t), (text, t)
            assert same(jaro.loads(text.encode("utf-8")), t)
            blob = jaro.dumps_binary(t, dedup=dedup)
            assert same(jaro.loads_binary(blob), t), (blob.hex(), t)


def test_text_and_binary_agree() -> None:
    r = random.Random(SEED + 1)
    for _ in range(TREES):
        t = tree(r)
        assert same(jaro.loads(jaro.dumps(t)), jaro.loads_binary(jaro.dumps_binary(t)))


def test_encoding_is_deterministic_and_canonical() -> None:
    r = random.Random(SEED + 2)
    for _ in range(100):
        t = tree(r)
        text = jaro.dumps(t)
        assert jaro.dumps(t) == text
        assert jaro.dumps(jaro.loads(text)) == text
        blob = jaro.dumps_binary(t)
        assert jaro.dumps_binary(jaro.loads_binary(blob)) == blob


def test_dedup_shrinks_repeated_subtrees() -> None:
    sub = {"name": "repeated sub-tree", "tags": ["a", "b", "c"], "n": 12345}
    t = {"a": sub, "b": sub, "c": sub}
    assert len(jaro.dumps(t, dedup=True)) < len(jaro.dumps(t))
    assert "&a0" in jaro.dumps(t, dedup=True)
    assert len(jaro.dumps_binary(t, dedup=True)) < len(jaro.dumps_binary(t))
