"""Anchor planning for repeated sub-trees (the dedup option)."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from ._core import sorted_keys
from .errors import JaroError
from .tables import table_keys

DEFAULT_MIN_DEDUP = 24  # smallest sub-tree, in text bytes, worth anchoring


class Dedup:
    """Counts sub-trees by their text form; ref() is called once per container at emission time."""

    def __init__(
        self, root: Any, text_of: Callable[[Any], str], column_major: bool
    ) -> None:
        self._text_of = text_of
        self._col = column_major
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
            k = self._key(v)
            if k is not None:
                c = self._counts.get(k, 0) + 1
                self._counts[k] = c
                if c > 1:
                    continue
            if isinstance(v, dict):
                stack.extend(v[key] for key in reversed(sorted_keys(v)))
            elif isinstance(v, list):
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

    def ref(self, v: Any) -> tuple[int, bool] | None:
        """Return (index, True) to reference an anchor, (index, False) to define one, or None."""
        k = self._key(v)
        if k is None or self._counts.get(k, 0) < 2:
            return None
        i = self._names.get(k)
        if i is not None:
            return i, True
        i = len(self._names)
        self._names[k] = i
        return i, False
