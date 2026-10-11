"""Binary column kinds: classification and numeric helpers."""

from __future__ import annotations

import math
from typing import Any

from ._core import MAX_EXACT, number_kind, sorted_keys, wrap64
from .tables import table_data

GENERIC, DELTA, DECIMAL, DICT, BOOL, OBJECT = range(6)
MAX_DECIMALS = 6
MIN_COLUMN = 4  # arrays shorter than this stay plain
POW10 = (
    1.0,
    1e1,
    1e2,
    1e3,
    1e4,
    1e5,
    1e6,
    1e7,
    1e8,
    1e9,
    1e10,
    1e11,
    1e12,
    1e13,
    1e14,
    1e15,
)


class Plan:
    """How one column is stored."""

    __slots__ = ("kind", "k", "dict", "idx", "keys", "cols")

    def __init__(self, kind: int, k: int = 0) -> None:
        self.kind = kind
        self.k = k
        self.dict: list[str] = []
        self.idx: dict[str, int] = {}
        self.keys: list[str] = []
        self.cols: list[list[Any]] = []


def zigzag(v: int) -> int:
    """Map an int64 to its unsigned zigzag form."""
    return (v << 1) ^ (v >> 63)


def zigzag_len(v: int) -> int:
    u = zigzag(v)
    n = 1
    while u >= 0x80:
        u >>= 7
        n += 1
    return n


def round_half_away(x: float) -> int:
    ax = abs(x)
    r = math.floor(ax)
    if ax - r >= 0.5:
        r += 1
    return -r if x < 0 else r


def decimals(f: float) -> int:
    """Return the fewest decimal places (<= MAX_DECIMALS) that represent f exactly as m/10^k, or -1."""
    for k in range(MAX_DECIMALS + 1):
        p = POW10[k]
        y = f * p
        if abs(y) < MAX_EXACT and round_half_away(y) / p == f:
            return k
    return -1


def classify(vals: list[Any]) -> Plan:
    """Pick the cheapest lossless column kind for vals."""
    n = len(vals)
    if n == 0:
        return Plan(GENERIC)
    first = vals[0]
    if isinstance(first, bool):
        for v in vals:
            if not isinstance(v, bool):
                return Plan(GENERIC)
        return Plan(BOOL)
    if isinstance(first, str):
        return _classify_strings(vals)
    if isinstance(first, dict):
        got = table_data(vals)
        if got is None and n == 1 and first:
            keys = sorted_keys(first)
            got = keys, [[first[k]] for k in keys]
        if got is None:
            return Plan(GENERIC)
        p = Plan(OBJECT)
        p.keys, p.cols = got
        return p
    return _classify_numbers(vals)


def _classify_strings(vals: list[Any]) -> Plan:
    idx: dict[str, int] = {}
    dictionary: list[str] = []
    n = len(vals)
    for v in vals:
        if not isinstance(v, str):
            return Plan(GENERIC)
        if v not in idx:
            idx[v] = len(dictionary)
            dictionary.append(v)
            if (
                len(dictionary) * 2 > n
            ):  # distinct count only grows: not dictionary-worthy
                return Plan(GENERIC)
    if n >= MIN_COLUMN and len(dictionary) * 2 <= n:
        p = Plan(DICT)
        p.dict, p.idx = dictionary, idx
        return p
    return Plan(GENERIC)


def _classify_numbers(vals: list[Any]) -> Plan:
    nums: list[int | float] = []
    all_int = True
    k = 0
    for v in vals:
        x = number_kind(v)
        if x is None:
            return Plan(GENERIC)
        nums.append(x)
        if isinstance(x, int):
            continue
        all_int = False
        kk = decimals(x)
        if kk < 0:
            return Plan(GENERIC)
        k = max(k, kk)
    if all_int:
        plain = delta = 0
        prev = 0
        for x in nums:
            i = int(x)
            plain += zigzag_len(i)
            delta += zigzag_len(wrap64(i - prev))
            prev = i
        return Plan(DELTA if delta < plain else DECIMAL)
    p = POW10[k]
    for x in nums:
        if isinstance(x, int):
            if abs(float(x)) * p >= MAX_EXACT:
                return Plan(GENERIC)
        elif float(round_half_away(x * p)) / p != x:  # mixed scales can lose digits
            return Plan(GENERIC)
    return Plan(DECIMAL, k)
