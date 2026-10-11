"""Geometric-mean decode metric: mean over 12 cells of time / baseline time (lower is better).

Usage: python bench/decode_gm.py [--runs N] [--write-baseline] [--table]
Each cell is the per-cell minimum over N passes of decode_metric.min_ms. Prints one number
(the geometric mean ratio) and, on stderr, the worst cell regression. --write-baseline stores
the cell times in bench/decode_baseline.json.
"""

from __future__ import annotations

import json
import math
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import decode_metric as dm  # noqa: E402

BASELINE = Path(__file__).resolve().parent / "decode_baseline.json"


def measure(runs: int) -> dict[str, float]:
    cells: dict[str, float] = {}
    for _ in range(runs):
        for p in sorted(dm.CORPUS.glob("*.json")):
            v = json.loads(p.read_text(encoding="utf-8"))
            text = dm.jaro.dumps(v)
            blob = dm.jaro.dumps_binary(v)
            for kind, fn in (
                ("text", lambda: dm.jaro.loads(text)),
                ("bin", lambda: dm.jaro.loads_binary(blob)),
            ):
                key = f"{p.stem}/{kind}"
                cells[key] = min(cells.get(key, math.inf), dm.min_ms(fn))
    return cells


def main() -> None:
    runs = int(sys.argv[sys.argv.index("--runs") + 1]) if "--runs" in sys.argv else 1
    cells = measure(runs)
    if "--json" in sys.argv:
        print(json.dumps(cells))
        return
    if "--write-baseline" in sys.argv:
        BASELINE.write_text(json.dumps(cells, indent=1, sort_keys=True) + "\n")
        print(f"wrote {BASELINE}")
        return
    base: dict[str, float] = json.loads(BASELINE.read_text())
    ratios = {k: cells[k] / base[k] for k in base}
    if "--table" in sys.argv:
        for k, r in ratios.items():
            print(f"{k:22s} {cells[k]:9.3f} ms  ratio {r:.3f}", file=sys.stderr)
    worst = max(ratios, key=lambda k: ratios[k])
    print(f"worst {worst} {ratios[worst]:.3f}", file=sys.stderr)
    print(f"{math.exp(sum(map(math.log, ratios.values())) / len(ratios)):.4f}")


if __name__ == "__main__":
    main()
