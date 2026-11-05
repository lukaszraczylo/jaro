"""Anchor planning for repeated sub-trees (the dedup option)."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from ._core import ABSENT, sorted_keys
from .errors import JaroError
from .tables import ColSpec, keyed_specs, leaf_cells, table_keys, table_specs

DEFAULT_MIN_DEDUP = 8  # smallest sub-tree, in text bytes, worth anchoring
MIN_ANCHOR_STR = 3  # shorter strings cost under 3 tokens even when quoted
REF_TOKENS = 2
DEF_TOKENS = 2


def tok_cost(s: str, quoted: bool) -> int:
    """Estimate the o200k_base tokens of s as the text encoder writes it."""
    c = 2 if quoted else 1
    i, n = 0, len(s)
    while i < n:
        ch = s[i]
        if ch >= "\x80":
            c += 1
            i += 1
        elif "0" <= ch <= "9":
            j = i
            while j < n and "0" <= s[j] <= "9":
                j += 1
            c += (j - i + 2) // 3
            i = j
        elif "a" <= ch <= "z" or "A" <= ch <= "Z":
            j = i
            while j < n and ("a" <= s[j] <= "z" or "A" <= s[j] <= "Z"):
                j += 1
            c += (j - i + 5) // 6
            i = j
        else:
            c += 1
            i += 1
    return c


class Dedup:
    """Counts sub-trees by their text form; ref() is called once per container at emission time."""

    def __init__(
        self,
        root: Any,
        text_of: Callable[[Any], str],
        column_major: bool,
        quote: Callable[[str], bool] | None = None,
    ) -> None:
        self._text_of = text_of
        self._col = column_major
        self._quote = quote if not column_major else None
        self._next = 0
        self._str_n: dict[str, int] = {}
        self._str_idx: dict[str, int] = {}
        self._counts: dict[str, int] = {}
        self._names: dict[str, int] = {}
        self._keys: dict[int, str | None] = {}
        self._visit(root)

    def _key(self, v: Any) -> str | None:
        if not isinstance(v, (dict, list)):
            return None
        slot = id(v)
        if slot in self._keys:
            return self._keys[slot]
        try:
            text = self._text_of(v)
        except JaroError:
            key = None
        else:
            size = len(text) if text.isascii() else len(text.encode("utf-8"))
            key = text if size >= DEFAULT_MIN_DEDUP else None
        self._keys[slot] = key
        return key

    def _visit(self, root: Any) -> None:
        # Pre-order walk with an explicit stack: children are pushed in reverse.
        stack: list[Any] = [root]
        while stack:
            v = stack.pop()
            if isinstance(v, str):
                if (
                    self._quote is not None
                    and len(v.encode("utf-8", "surrogatepass")) >= MIN_ANCHOR_STR
                ):
                    self._str_n[v] = self._str_n.get(v, 0) + 1
                continue
            k = self._key(v)
            if k is not None:
                c = self._counts.get(k, 0) + 1
                self._counts[k] = c
                if c > 1:
                    continue
            if isinstance(v, dict):
                names = sorted_keys(v)
                if self._quote is not None:
                    specs = keyed_specs(v)
                    if specs is not None:
                        self._push_cells(stack, specs, [v[n] for n in names])
                        continue
                stack.extend(v[key] for key in reversed(names))
            elif isinstance(v, list):
                if self._quote is not None:
                    specs = table_specs(v)
                    if specs is not None:
                        self._push_cells(stack, specs, v)
                        continue
                cols = table_keys(v)
                if cols is None:
                    stack.extend(reversed(v))
                elif self._col:
                    stack.extend(
                        v[i][c] for c in reversed(cols) for i in reversed(range(len(v)))
                    )
                else:
                    stack.extend(
                        v[i][c] for i in reversed(range(len(v))) for c in reversed(cols)
                    )

    @staticmethod
    def _push_cells(stack: list[Any], specs: list[ColSpec], rows: list[Any]) -> None:
        """Push the cells of rows so they pop in the order the text encoder writes them."""
        cells: list[Any] = []
        for r in rows:
            leaf_cells(specs, r, cells)
        stack.extend(c for c in reversed(cells) if c is not ABSENT)

    def ref_str(self, s: str) -> tuple[int, bool] | None:
        """Like ref, for a string value."""
        if (
            self._quote is None
            or len(s.encode("utf-8", "surrogatepass")) < MIN_ANCHOR_STR
        ):
            return None
        n = self._str_n.get(s, 0)
        if n < 2 or (n - 1) * (tok_cost(s, self._quote(s)) - REF_TOKENS) <= DEF_TOKENS:
            return None
        i = self._str_idx.get(s)
        if i is not None:
            return i, True
        i = self._next
        self._next += 1
        self._str_idx[s] = i
        return i, False

    def ref(self, v: Any) -> tuple[int, bool] | None:
        """Return (index, True) to reference an anchor, (index, False) to define one, or None."""
        k = self._key(v)
        if k is None or self._counts.get(k, 0) < 2:
            return None
        i = self._names.get(k)
        if i is not None:
            return i, True
        i = self._next
        self._next += 1
        self._names[k] = i
        return i, False
