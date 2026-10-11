"""The text decoder fast paths must agree with the plain scanner on value, type and error."""

from __future__ import annotations

import random
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from typing import Any

import pytest
from test_roundtrip import tree

import jaro
from jaro import JaroError
from jaro import text as text_mod

SEED = 20261012
TREES = 2000
MUTATIONS = 2000
ADVERSARIAL = [
    "",
    " ",
    '"',
    "\\",
    "\\n",
    '\\"',
    "a\tb",
    "line\r\nbreak",
    "\x00",
    "\x01\x02",
    "\x7f",
    "é",
    " ",
    " ",
    "😀",
    "퟿",
    "",
    "^",
    "~",
    "&a",
    "*a",
    "true",
    "false",
    "007",
    "-0",
    ".5",
    "1.",
    "1e5",
    "9" * 30,
    "[]",
    "{}",
    "[a]",
    "{a:b}",
    "a:b",
    "a|b",
    "#c",
    "[|a|",
    "a]",
    "a}",
]


@contextmanager
def plain_scanner() -> Iterator[None]:
    """Disable the regex row, matrix and JSON paths so only the token scanner runs."""
    mp = pytest.MonkeyPatch()

    def no_rows(k: int) -> Callable[[str, int], None]:
        return lambda s, pos: None

    def old_float_rows(s: str, pos: int) -> Any:
        end = s.find("]", pos + 2)
        if end < 0:
            return None
        k = s.count(" ", pos + 2, end) + 1
        if k > text_mod._MAX_ROW:
            return None
        return text_mod._float_rows_re(s, pos, k)

    mp.setattr(text_mod, "_row_match", no_rows)
    mp.setattr(text_mod, "_float_rows", old_float_rows)
    try:
        yield
    finally:
        mp.undo()


def outcome(fn: Callable[[str], Any], s: str) -> tuple[str, str]:
    try:
        return ("ok", repr(fn(s)))
    except JaroError as e:
        return ("err", str(e))
    except RecursionError:
        return ("recursion", "")


def both(s: str) -> tuple[tuple[str, str], tuple[str, str]]:
    new = outcome(jaro.loads, s)
    with plain_scanner():
        old = outcome(jaro.loads, s)
    return new, old


def adversarial_tree(r: random.Random) -> Any:
    if r.random() < 0.25:
        k = r.randint(1, 5)
        return [
            [
                round(r.uniform(-500, 500), 4) if r.random() < 0.8 else r.randint(-9, 9)
                for _ in range(k)
            ]
            for _ in range(r.randint(1, 6))
        ]
    if r.random() < 0.4:
        keys = [r.choice(ADVERSARIAL) + str(i) for i in range(r.randint(1, 4))]
        return [
            {
                k: r.choice(
                    [*ADVERSARIAL, 1, 2.5, None, True, [], {}, [1, "x"], [1.5, 2.5]]
                )
                for k in keys
                if r.random() < 0.85
            }
            for _ in range(r.randint(2, 6))
        ]
    return tree(r, 0)


def test_random_trees_match_plain_scanner() -> None:
    r = random.Random(SEED)
    for i in range(TREES):
        v = adversarial_tree(r)
        s = jaro.dumps(v)
        new, old = both(s)
        assert new == old, (i, s)
        assert new[0] == "ok", (i, s)


def test_mutated_texts_match_plain_scanner() -> None:
    r = random.Random(SEED + 1)
    junk = list(' \t\n"\\[]{}|:#^~&*,.-e0123456789ab')
    for i in range(MUTATIONS):
        s = jaro.dumps(adversarial_tree(r))
        for _ in range(r.randint(1, 3)):
            if not s:
                break
            p = r.randrange(len(s))
            op = r.random()
            if op < 0.4:
                s = s[:p] + r.choice(junk) + s[p + 1 :]
            elif op < 0.7:
                s = s[:p] + r.choice(junk) + s[p:]
            else:
                s = s[:p] + s[p + 1 :]
        new, old = both(s)
        assert new == old, (i, s)


@pytest.mark.parametrize(
    "s",
    [
        "[|a b| 1 2 3 4]",
        "[|a| 1 ^ 3]",
        "[|a b| x [1 2] y {}]",
        "[|a b| x [1 ^] y []]",
        "[|a b| x [1 &r] y []]",
        "[|a b| x [[1] 2] y []]",
        '[|a b| "x\\ny" 1 "z" 2]',
        "[|a b| 1 2 3]",
        "[|a b| 1 2 3 4 ]",
        "[|a b|]",
        "{|a b| r1 1 2 r2 3 4}",
        "{|a b| r1 1 2 r1 3 4}",
        "[[1.5 2.5] [3.5 4.5]]",
        "[[1.5 2] [3 4.5]]",
        "[[1.0 2.5] [3.5 4.5]]",
        "[[1.5 2.5]  [3.5 4.5]]",
        "[[1.5 1e3] [3.5 4.5]]",
        "[[1.5 1.5e-3] [3.5 4.5]]",
        "[[1.5 007] [3.5 4.5]]",
        "[[1.5 .5] [3.5 1.]]",
        "[[1.5 true] [3.5 4.5]]",
        "[[1.5 ~] [3.5 4.5]]",
        '[[1.5 "a"] [3.5 4.5]]',
        "[[1.5 NaN] [3.5 4.5]]",
        "[[1.5 Infinity] [3.5 4.5]]",
        "[[1.5 -0] [3.5 4.5]]",
        "[[1.5 123456789012345678] [3.5 4.5]]",
        "[[1.5 1000000000000000.5] [3.5 4.5]]",
        "[[1.5 2.5] [3.5]]",
        "[[1.5 2.5]]]",
        "[[[1.5 2.5] [3.5 4.5]]]",
    ],
)
def test_edge_texts_match_plain_scanner(s: str) -> None:
    new, old = both(s)
    assert new == old


def test_row_cell_depth_limit_matches_plain_scanner() -> None:
    for depth in range(500, 516):
        s = "[" * depth + "[|a b| x [1 2] y []]" + "]" * depth
        new, old = both(s)
        assert new == old, depth
