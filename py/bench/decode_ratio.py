"""Ratio metric: JARO decode time / json.loads time per cell, geometric mean over 12 cells.

Usage: python bench/decode_ratio.py [--rounds N] [--table] [--write-baseline]
Each cell alternates rounds of the JARO decode and json.loads on the same file in one process and
keeps the minimum of each, so machine noise cancels. gc is disabled inside the timed loops only.
"""

from __future__ import annotations

import gc
import json
import math
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
BASELINE = Path(__file__).resolve().parent / "ratio_baseline.json"
TARGET_S = 0.04


def timed(fn: Callable[[], Any], iters: int) -> float:
    gc.collect()
    gc.disable()
    try:
        t0 = time.perf_counter()
        for _ in range(iters):
            fn()
        return (time.perf_counter() - t0) / iters
    finally:
        gc.enable()


def pair(
    a: Callable[[], Any], b: Callable[[], Any], rounds: int
) -> tuple[float, float]:
    a()
    b()
    t0 = time.perf_counter()
    a()
    one = max(time.perf_counter() - t0, 1e-6)
    ia = max(1, round(TARGET_S / one))
    t0 = time.perf_counter()
    b()
    ib = max(1, round(TARGET_S / max(time.perf_counter() - t0, 1e-6)))
    best_a = best_b = math.inf
    for _ in range(rounds):
        best_a = min(best_a, timed(a, ia))
        best_b = min(best_b, timed(b, ib))
    return best_a, best_b


def measure(rounds: int) -> dict[str, float]:
    ratios: dict[str, float] = {}
    for p in sorted(CORPUS.glob("*.json")):
        raw = p.read_text(encoding="utf-8")
        v = json.loads(raw)
        text = jaro.dumps(v)
        blob = jaro.dumps_binary(v)
        for kind, fn in (
            ("text", lambda: jaro.loads(text)),
            ("bin", lambda: jaro.loads_binary(blob)),
        ):
            a, b = pair(fn, lambda: json.loads(raw), rounds)
            ratios[f"{p.stem}/{kind}"] = a / b
    return ratios


def geomean(r: dict[str, float]) -> float:
    return math.exp(sum(map(math.log, r.values())) / len(r))


def main() -> None:
    rounds = (
        int(sys.argv[sys.argv.index("--rounds") + 1]) if "--rounds" in sys.argv else 7
    )
    ratios = measure(rounds)
    if "--write-baseline" in sys.argv:
        BASELINE.write_text(json.dumps(ratios, indent=1, sort_keys=True) + "\n")
        print(f"wrote {BASELINE}  geomean {geomean(ratios):.4f}")
        return
    base: dict[str, float] = (
        json.loads(BASELINE.read_text()) if BASELINE.exists() else {}
    )
    if "--table" in sys.argv:
        for k, r in ratios.items():
            extra = f"  vs baseline {r / base[k]:.3f}" if k in base else ""
            print(f"{k:22s} ratio {r:.3f}{extra}", file=sys.stderr)
    if base:
        worst = max(ratios, key=lambda k: ratios[k] / base[k])
        print(
            f"worst vs baseline {worst} {ratios[worst] / base[worst]:.3f}; geomean vs baseline {geomean(ratios) / geomean(base):.4f}",
            file=sys.stderr,
        )
    print(f"{geomean(ratios):.4f}")


if __name__ == "__main__":
    main()
