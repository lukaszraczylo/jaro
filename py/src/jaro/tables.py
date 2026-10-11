"""Table detection and column specs shared by the text and binary encoders."""

from __future__ import annotations

from collections.abc import Iterator
from typing import Any

from ._core import ABSENT, sorted_keys

MAX_NEST = 8  # deepest nesting of object columns in one table header
TEXT_PRESENT = 0.5  # smallest share of present cells for a text sparse table
BIN_PRESENT = 0.8  # smallest share of present cells for a binary sparse table


class ColSpec:
    """One table column. A column with sub-columns holds an object in every row."""

    __slots__ = ("key", "sub")

    def __init__(self, key: str, sub: list[ColSpec] | None = None) -> None:
        self.key = key
        self.sub = sub


def build_specs(rows: list[Any], depth: int) -> list[ColSpec] | None:
    """Return the columns for rows (all dicts) when enough cells are present for a table."""
    info: dict[str, list[Any]] = (
        {}
    )  # key -> [rows with key, some value is not an object, objects]
    present = 0
    for r in rows:
        present += len(r)
        for k, v in r.items():
            c = info.get(k)
            if c is None:
                c = info[k] = [0, False, []]
            c[0] += 1
            if isinstance(v, dict):
                c[2].append(v)
            else:
                c[1] = True
    if not info or present < TEXT_PRESENT * (len(info) * len(rows)):
        return None
    nrows = len(rows)
    specs: list[ColSpec] = []
    for k in sorted_keys(info):
        c = info[k]
        sub = None
        if depth < MAX_NEST and not c[1] and c[0] == nrows:
            got = build_specs(c[2], depth + 1)
            if got is not None and len(got) >= 2:
                sub = got
        specs.append(ColSpec(k, sub))
    return specs


def table_specs(rows: list[Any]) -> list[ColSpec] | None:
    """Return the columns when rows can be written as a text table."""
    if len(rows) < 2:
        return None
    for r in rows:
        if not isinstance(r, dict):
            return None
    return build_specs(rows, 0)


def keyed_specs(m: dict[Any, Any]) -> list[ColSpec] | None:
    """Return the columns when every value of m is an object and a table of them has 2+ keys."""
    if len(m) < 2:
        return None
    rows = list(m.values())
    for r in rows:
        if not isinstance(r, dict):
            return None
    specs = build_specs(rows, 0)
    if specs is None or len(specs) < 2:
        return None
    return specs


def flat_keys(specs: list[ColSpec]) -> list[str] | None:
    """Return the keys when no column has sub-columns."""
    keys: list[str] = []
    for s in specs:
        if s.sub is not None:
            return None
        keys.append(s.key)
    return keys


def leaf_cells(specs: list[ColSpec], row: dict[str, Any], acc: list[Any]) -> None:
    """Append one cell per leaf column of row: its value, or ABSENT."""
    for s in specs:
        if s.sub is not None:
            leaf_cells(s.sub, row[s.key], acc)
        else:
            acc.append(row.get(s.key, ABSENT))


def leaf_count(specs: list[ColSpec]) -> int:
    return sum(leaf_count(s.sub) if s.sub is not None else 1 for s in specs)


def build_row(specs: list[ColSpec], cells: Iterator[Any]) -> dict[str, Any]:
    """Build a row object from leaf cells; a sub-column object exists even when all its cells are absent."""
    row: dict[str, Any] = {}
    for s in specs:
        if s.sub is not None:
            row[s.key] = build_row(s.sub, cells)
        else:
            v = next(cells)
            if v is not ABSENT:
                row[s.key] = v
    return row


def sparse_keys(a: list[Any], min_present: float) -> list[str] | None:
    """Return the union of keys when a holds 2+ objects and enough cells would be present."""
    if len(a) < 2:
        return None
    seen: set[str] = set()
    present = 0
    for x in a:
        if not isinstance(x, dict):
            return None
        present += len(x)
        seen.update(x)
    if not seen or present < min_present * (len(seen) * len(a)):
        return None
    return sorted_keys(dict.fromkeys(seen))


def table_data(a: list[Any]) -> tuple[list[str], list[list[Any]]] | None:
    """Return the sorted shared keys and the column-major cells of 2+ same-key objects."""
    if len(a) < 2:
        return None
    first = a[0]
    if not isinstance(first, dict) or not first:
        return None
    keys = sorted_keys(first)
    n = len(keys)
    rows: list[list[Any]] = []
    for x in a:
        if not isinstance(x, dict) or len(x) != n:
            return None
        try:
            rows.append([x[k] for k in keys])
        except KeyError:
            return None
    return keys, [list(c) for c in zip(*rows)]


def table_keys(a: list[Any]) -> list[str] | None:
    got = table_data(a)
    return None if got is None else got[0]
