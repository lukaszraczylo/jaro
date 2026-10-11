"""Limits and helpers shared by the text and binary codecs."""

from __future__ import annotations

import math
from typing import Any

from .errors import JaroError

MAX_DEPTH = 512
MAX_EXPAND = 1 << 24  # nodes a decoder may materialise through references
MAX_EXACT = 1 << 53
INT64_MIN = -(1 << 63)
INT64_MAX = (1 << 63) - 1
_U64 = (1 << 64) - 1
_NOT_STR = "object keys must be strings"
_CONTAINERS: frozenset[type] = frozenset((dict, list))


class _Absent:
    """Marks a table cell for a key the row does not have."""

    __slots__ = ()

    def __repr__(self) -> str:
        return "ABSENT"


ABSENT: Any = _Absent()


def wrap64(x: int) -> int:
    """Wrap x to the int64 range, as Go integer arithmetic does."""
    return ((x + (1 << 63)) & _U64) - (1 << 63)


def sorted_keys(d: dict[Any, Any]) -> list[str]:
    """Return the keys of d in code-point order; raise unless all are str."""
    try:
        keys = sorted(d)
    except TypeError:
        raise JaroError(_NOT_STR) from None
    if keys:
        kinds = set(map(type, keys))
        if kinds != {str} and not all(issubclass(t, str) for t in kinds):
            raise JaroError(_NOT_STR)
    return keys


def norm_int(v: int) -> int:
    if v < INT64_MIN or v > INT64_MAX:
        raise JaroError(f"integer {v} does not fit in int64")
    return v


def norm_float(f: float) -> int | float:
    """Apply the data model rule: finite floats, integral ones below 2^53 are ints."""
    if f != f or f in (math.inf, -math.inf):
        raise JaroError("NaN/Inf not representable")
    if f.is_integer() and -MAX_EXACT < f < MAX_EXACT:
        return int(f)
    return f


def number_kind(v: Any) -> int | float | None:
    """Return v as a normalised int or float, or None when v is not a number."""
    if isinstance(v, bool):
        return None
    if isinstance(v, int):
        return norm_int(int(v))
    if isinstance(v, float):
        return norm_float(float(v))
    return None


def as_number(v: Any) -> int | float:
    """Return v as a normalised number; the caller has already classified the column as numeric."""
    x = number_kind(v)
    if x is None:
        raise JaroError(f"unsupported type {type(v).__name__}")
    return x


def deep_copy(v: Any, budget: int) -> tuple[Any, int]:
    """Copy containers without recursion. A negative second result means the budget ran out."""
    if not isinstance(v, (dict, list)):
        return v, budget
    budget -= len(v) + 1
    if budget < 0:
        return None, budget
    root: Any = {} if isinstance(v, dict) else []
    work: list[tuple[Any, Any]] = [(v, root)]
    while work:
        src, dst = work.pop()
        is_dict = isinstance(src, dict)
        items = src.values() if is_dict else src
        if _CONTAINERS.isdisjoint({type(x) for x in items}):
            if is_dict:
                dst.update(src)
            else:
                dst.extend(src)
            continue
        for k, el in src.items() if is_dict else enumerate(src):
            if isinstance(el, (dict, list)):
                budget -= len(el) + 1
                if budget < 0:
                    return None, budget
                c: Any = {} if isinstance(el, dict) else []
                work.append((el, c))
                el = c
            if is_dict:
                dst[k] = el
            else:
                dst.append(el)
    return root, budget
