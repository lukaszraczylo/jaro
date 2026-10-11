"""Differential and safety tests for jaro_native against the pure-Python decoder."""

from __future__ import annotations

import gc
import os
import random
import subprocess
import threading

import jaro_native
import pytest
from helpers import deep_equal
from jaro.binary import loads_binary as pure_loads

from jaro import dumps_binary

native = jaro_native.loads_binary
N_TREES = 2500
N_MUTANTS = 30000


def outcome(fn, data: bytes):
    try:
        return ("ok", fn(data))
    except ValueError as e:
        return ("err", str(e))


def same_outcome(data: bytes) -> bool:
    a, b = outcome(native, data), outcome(pure_loads, data)
    if a[0] != b[0]:
        return False
    return deep_equal(a[1], b[1]) if a[0] == "ok" else a[1] == b[1]


# --- conformance vectors -----------------------------------------------------


def test_binary_vectors(vectors):
    assert len(vectors["binary"]) >= 30
    for case in vectors["binary"]:
        data = bytes.fromhex(case["hex"])
        got = native(data)
        assert deep_equal(got, pure_loads(data)), case["name"]
        assert got == case["json"], case["name"]


def test_bad_binary_vectors(vectors):
    assert len(vectors["badBinaryHex"]) >= 10
    for h in vectors["badBinaryHex"]:
        data = bytes.fromhex(h)
        with pytest.raises(jaro_native.JaroError) as ei:
            native(data)
        assert isinstance(ei.value, ValueError)
        assert same_outcome(data), h


def test_error_has_offset():
    with pytest.raises(jaro_native.JaroError) as ei:
        native(b"\x0f\x01\x07\x05")
    assert ei.value.offset == 4
    assert "truncated" in ei.value.message


def test_header_errors():
    for data in (b"", b"\x0f", b"\x0e\x01\x00", b"\x0f\x02\x00", b"\x0f\x01\x00\x00"):
        assert same_outcome(data)
        with pytest.raises(ValueError):
            native(data)


def test_accepts_bytearray_and_memoryview():
    data = dumps_binary({"a": [1, 2, 3, 4, 5]})
    assert native(bytearray(data)) == native(memoryview(data)) == {"a": [1, 2, 3, 4, 5]}
    with pytest.raises(TypeError):
        native("text")  # type: ignore[arg-type]


# --- corpus ------------------------------------------------------------------


def test_corpus(corpus_binaries):
    assert len(corpus_binaries) == 6
    for name, (blob, parsed) in corpus_binaries.items():
        got = native(blob)
        assert deep_equal(got, pure_loads(blob)), name
        assert got == parsed, name


# --- random trees ------------------------------------------------------------

WORDS = [
    "a",
    "b",
    "id",
    "name",
    "kind",
    "x",
    "yy",
    "alpha",
    "beta",
    "gamma",
    "δ",
    "日本",
    "é",
    "",
    "q" * 40,
]
INTS = [
    0,
    1,
    -1,
    127,
    128,
    255,
    256,
    2**31,
    -(2**31),
    2**53,
    2**53 + 1,
    2**63 - 1,
    -(2**63),
]
FLOATS = [
    0.5,
    -0.5,
    1.25,
    3.14159,
    1e300,
    -1e-300,
    2.0**53,
    2.0**60,
    0.1,
    123456.789,
    1e22,
    5e-324,
]


def scalar(r: random.Random):
    c = r.randrange(9)
    if c == 0:
        return None
    if c == 1:
        return r.random() < 0.5
    if c == 2:
        return r.choice(INTS)
    if c == 3:
        return r.randrange(-1000, 1000)
    if c == 4:
        return r.choice(FLOATS)
    if c == 5:
        return r.randrange(-(10**6), 10**6) / 100
    if c == 6:
        return float(r.randrange(-5, 300))
    return r.choice(WORDS) + (str(r.randrange(5)) if r.random() < 0.3 else "")


def tree(r: random.Random, depth: int):
    c = r.randrange(12)
    if depth <= 0 or c < 3:
        return scalar(r)
    if c < 5:
        return {r.choice(WORDS): tree(r, depth - 1) for _ in range(r.randrange(5))}
    if c < 7:
        return [tree(r, depth - 1) for _ in range(r.randrange(7))]
    keys = r.sample(WORDS, r.randrange(1, 5))
    n = r.randrange(2, 30)
    if c == 7:  # table or sparse table
        sparse = r.random() < 0.5
        return [
            {k: scalar(r) for k in keys if not sparse or r.random() < 0.85}
            or {keys[0]: 1}
            for _ in range(n)
        ]
    if c == 8:  # typed columns
        kind = r.randrange(6)
        if kind == 0:
            return [r.randrange(-(10**9), 10**9) for _ in range(n)]
        if kind == 1:
            return [r.randrange(-(10**6), 10**6) / 1000 for _ in range(n)]
        if kind == 2:
            return [r.choice(["red", "green", "blue"]) for _ in range(n)]
        if kind == 3:
            return [r.random() < 0.5 for _ in range(n)]
        if kind == 4:
            return [r.choice(INTS) for _ in range(n)]
        return [r.choice([*FLOATS, *INTS]) for _ in range(n)]
    if c == 9:  # object column
        return [
            {"p": {"q": scalar(r), "r": r.randrange(100)}, "s": r.choice(WORDS)}
            for _ in range(n)
        ]
    return [[r.random() for _ in range(3)] for _ in range(n)]


def test_random_trees_differential():
    r = random.Random(20261011)
    for i in range(N_TREES):
        value = tree(r, r.randrange(1, 5))
        blob = dumps_binary(value, dedup=(i % 5 == 0))
        got = native(blob)
        assert deep_equal(got, pure_loads(blob)), (i, value)


def test_random_trees_with_anchors_repeat_subtrees():
    r = random.Random(7)
    for i in range(300):
        sub = tree(r, 3)
        value = {"a": sub, "b": [sub, sub], "c": {"d": sub}}
        blob = dumps_binary(value, dedup=True)
        assert deep_equal(native(blob), pure_loads(blob)), i


def test_anchor_copies_are_independent():
    sub = {"k": [1, 2, 3, {"z": "w"}], "t": "text"}
    blob = dumps_binary({"a": sub, "b": sub}, dedup=True)
    out = native(blob)
    assert out["a"] == out["b"] and out["a"] is not out["b"]
    assert out["a"]["k"] is not out["b"]["k"]


def test_mutation_differential(corpus_binaries):
    r = random.Random(99)
    seeds = [dumps_binary(tree(r, 4), dedup=r.random() < 0.3) for _ in range(300)]
    seeds += [b for b, _ in corpus_binaries.values() if len(b) < 60000]
    for i in range(N_MUTANTS):
        b = bytearray(r.choice(seeds))
        for _ in range(r.randrange(1, 4)):
            m = r.randrange(4)
            if m == 0 and b:
                b[r.randrange(len(b))] ^= 1 << r.randrange(8)
            elif m == 1 and b:
                b[r.randrange(len(b))] = r.randrange(256)
            elif m == 2 and b:
                del b[r.randrange(len(b)) :]
            else:
                b.insert(r.randrange(len(b) + 1), r.randrange(256))
        assert same_outcome(bytes(b)), (i, bytes(b).hex())


# --- limits ------------------------------------------------------------------


def nested(depth: int) -> bytes:
    return b"\x0f\x01" + b"\x07\x01" * depth + b"\x00"


def test_depth_limit():
    for d in (100, 511, 512, 513, 600):
        assert same_outcome(nested(d)), d
    with pytest.raises(ValueError):
        native(nested(600))


def test_depth_limit_in_small_stack_thread():
    threading.stack_size(512 * 1024)
    res: list = []

    def run():
        for d in (512, 513):
            res.append(outcome(native, nested(d))[0])

    t = threading.Thread(target=run)
    t.start()
    t.join()
    threading.stack_size(0)
    assert res == ["ok", "err"]


def test_columns_and_tables_nest_depth():
    # object columns inside object columns, to the depth limit
    inner = b"\x00"
    for _ in range(300):
        inner = b"\x0c\x02\x05\x01\x41\x61" + inner  # col n=2 kind=object k=1 key "a"
    assert same_outcome(b"\x0f\x01" + inner)


def test_budget_and_count_limits():
    assert same_outcome(b"\x0f\x01\x07\xff\xff\xff\xff\x07")  # count above input
    assert same_outcome(b"\x0f\x01\x0c\xff\xff\xff\xff\x07\x00")  # huge column
    assert same_outcome(b"\x0f\x01\x0c\x80\x80\x80\x08\x04")  # bool column, 8x bound
    assert same_outcome(b"\x0f\x01\x07" + b"\xff" * 12)  # varint overflow
    assert same_outcome(b"\x0f\x01\x04" + b"\x00" * 6 + b"\xf8\x7f")  # NaN
    assert same_outcome(b"\x0f\x01\x04" + b"\x00" * 6 + b"\xf0\x7f")  # Inf


def test_invalid_utf8_and_surrogates():
    for body in (
        b"\xff",
        b"\xc0\x80",
        b"\xed\xa0\x80",
        b"\xf4\x90\x80\x80",
        b"\xe2\x82",
    ):
        data = b"\x0f\x01" + bytes([0x40 | len(body)]) + body
        assert same_outcome(data)
        with pytest.raises(ValueError):
            native(data)


# --- memory ------------------------------------------------------------------


def rss_kb() -> int:
    return int(
        subprocess.check_output(["ps", "-o", "rss=", "-p", str(os.getpid())]).split()[0]
    )


def growth_kb(fn, warm: int = 2000, rounds: int = 10000) -> int:
    for _ in range(warm):
        fn()
    gc.collect()
    base = rss_kb()
    for _ in range(rounds):
        fn()
    gc.collect()
    return rss_kb() - base


LEAK_LIMIT_KB = 2048


def test_leak_detector_detects_a_leak():
    keep: list = []
    assert (
        growth_kb(lambda: keep.append(bytearray(1000)), warm=100, rounds=10000)
        > LEAK_LIMIT_KB
    )


def test_no_leak_on_bad_vectors(vectors):
    bad = [bytes.fromhex(h) for h in vectors["badBinaryHex"]]

    def once():
        for b in bad:
            try:
                native(b)
            except ValueError:
                pass

    assert growth_kb(once) < LEAK_LIMIT_KB


def test_no_leak_on_mutated_inputs():
    r = random.Random(5)
    seeds = [dumps_binary(tree(r, 4), dedup=True) for _ in range(60)]
    muts = []
    for s in seeds:
        for _ in range(10):
            b = bytearray(s)
            b[r.randrange(len(b))] = r.randrange(256)
            muts.append(bytes(b[: r.randrange(len(b) // 2, len(b) + 1)]))

    def once():
        for b in muts:
            try:
                native(b)
            except ValueError:
                pass

    assert growth_kb(once, warm=20, rounds=100) < LEAK_LIMIT_KB


def test_no_leak_on_good_vectors(vectors):
    good = [bytes.fromhex(c["hex"]) for c in vectors["binary"]]

    def once():
        for b in good:
            native(b)

    assert growth_kb(once) < LEAK_LIMIT_KB
