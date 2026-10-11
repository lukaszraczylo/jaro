"""Decode metric: sum over the corpus files of the min decode ms, text loads + binary loads_binary.

Usage: python bench/decode_metric.py [--table]
Prints one number (ms, lower is better). gc is disabled around each timed loop here only,
to cut noise; the library is untouched. --table also prints per-file numbers on stderr.
"""

from __future__ import annotations

import gc
import json
import os
import sys
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

PY_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, os.environ.get("JARO_SRC", str(PY_ROOT / "src")))

import jaro  # noqa: E402

CORPUS = Path(os.environ.get("JARO_CORPUS", PY_ROOT.parent.parent / "corpus"))
RUNS = 9
TARGET_S = 0.05


def min_ms(fn: Callable[[], Any]) -> float:
    fn()
    t0 = time.perf_counter()
    fn()
    one = max(time.perf_counter() - t0, 1e-6)
    iters = max(1, round(TARGET_S / one))
    best = float("inf")
    for _ in range(RUNS):
        gc.collect()
        gc.disable()
        try:
            t0 = time.perf_counter()
            for _ in range(iters):
                fn()
            dt = (time.perf_counter() - t0) / iters
        finally:
            gc.enable()
        best = min(best, dt)
    return best * 1000.0


def main() -> None:
    total = 0.0
    rows = []
    for p in sorted(CORPUS.glob("*.json")):
        v = json.loads(p.read_text(encoding="utf-8"))
        text = jaro.dumps(v)
        blob = jaro.dumps_binary(v)
        t = min_ms(lambda: jaro.loads(text))
        b = min_ms(lambda: jaro.loads_binary(blob))
        rows.append((p.stem, t, b))
        total += t + b
    if "--table" in sys.argv:
        for name, t, b in rows:
            print(f"{name:15s} text {t:9.3f}  bin {b:9.3f}", file=sys.stderr)
    print(f"{total:.3f}")


if __name__ == "__main__":
    main()
