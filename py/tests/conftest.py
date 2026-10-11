from __future__ import annotations

from typing import Any


def same(a: Any, b: Any) -> bool:
    """Tree equality: equal numbers match across int/float, but bool never matches a number."""
    work = [(a, b)]
    while work:
        x, y = work.pop()
        if isinstance(x, bool) or isinstance(y, bool):
            if not (isinstance(x, bool) and isinstance(y, bool) and x == y):
                return False
        elif isinstance(x, (int, float)) and isinstance(y, (int, float)):
            if x != y:
                return False
        elif isinstance(x, dict) and isinstance(y, dict):
            if x.keys() != y.keys():
                return False
            work.extend((x[k], y[k]) for k in x)
        elif isinstance(x, list) and isinstance(y, list):
            if len(x) != len(y):
                return False
            work.extend(zip(x, y))
        elif type(x) is not type(y) or x != y:
            return False
    return True
