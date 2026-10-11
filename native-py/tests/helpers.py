"""Shared test helpers."""

from __future__ import annotations


def deep_equal(a: object, b: object) -> bool:
    """Equality that tells bool from int and int from float."""
    stack = [(a, b)]
    while stack:
        x, y = stack.pop()
        if type(x) is not type(y):
            return False
        if isinstance(x, dict):
            if x.keys() != y.keys():  # type: ignore[attr-defined]
                return False
            stack.extend((v, y[k]) for k, v in x.items())  # type: ignore[index]
        elif isinstance(x, list):
            if len(x) != len(y):  # type: ignore[arg-type]
                return False
            stack.extend(zip(x, y))  # type: ignore[call-overload]
        elif x != y:
            return False
    return True
