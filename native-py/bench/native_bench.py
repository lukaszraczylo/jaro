"""Per corpus file: min-of-rounds ms for json.loads, pure loads_binary, native loads_binary.

The three calls run interleaved in one process; the ratio is native / json.
"""

from __future__ import annotations

import gc
import json
import math
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
JARO = ROOT.parent
sys.path.insert(0, str(JARO / "py" / "src"))

import jaro_native  # noqa: E402
from jaro.binary import dumps_binary
from jaro.binary import loads_binary as pure_loads  # noqa: E402

CORPUS = JARO.parent / "corpus"
DATA = ROOT / "tests" / "data"
ROUNDS = int(sys.argv[1]) if len(sys.argv) > 1 else 15
TARGET_S = 0.04  # inner loop sized so one round of json.loads takes about this long


def timed(fn, arg, inner: int) -> float:
    t0 = time.perf_counter()
    for _ in range(inner):
        fn(arg)
    return (time.perf_counter() - t0) / inner * 1000


def load_inputs() -> list[tuple[str, str, bytes]]:
    out = []
    for src in sorted(CORPUS.glob("*.json")):
        text = src.read_text()
        blob_path = DATA / f"{src.stem}.jarob"
        blob = (
            blob_path.read_bytes()
            if blob_path.exists()
            else dumps_binary(json.loads(text))
        )
        out.append((src.stem, text, blob))
    return out


def main() -> None:
    gc.disable()
    rows = []
    for name, text, blob in load_inputs():
        assert jaro_native.loads_binary(blob) == json.loads(text)
        one = timed(json.loads, text, 1)
        inner = max(1, int(TARGET_S * 1000 / max(one, 1e-3)))
        best = {"json": math.inf, "pure": math.inf, "native": math.inf}
        for _ in range(ROUNDS):
            gc.collect()
            best["json"] = min(best["json"], timed(json.loads, text, inner))
            best["native"] = min(
                best["native"], timed(jaro_native.loads_binary, blob, inner)
            )
            best["pure"] = min(
                best["pure"], timed(pure_loads, blob, max(1, inner // 10))
            )
            best["native"] = min(
                best["native"], timed(jaro_native.loads_binary, blob, inner)
            )
        rows.append((name, len(text), len(blob), best))
    print(
        f"{'file':<14}{'json KB':>9}{'bin KB':>8}{'json ms':>10}{'pure ms':>10}{'native ms':>11}{'native/json':>13}{'pure/json':>11}"
    )
    ratios = []
    for name, jl, bl, b in rows:
        r = b["native"] / b["json"]
        ratios.append(r)
        print(
            f"{name:<14}{jl / 1024:>9.0f}{bl / 1024:>8.0f}{b['json']:>10.3f}{b['pure']:>10.3f}{b['native']:>11.3f}{r:>13.3f}{b['pure'] / b['json']:>11.2f}"
        )
    geo = math.exp(sum(map(math.log, ratios)) / len(ratios))
    print(f"geomean native/json = {geo:.4f}")
    print(
        "RATIOS "
        + json.dumps({n: round(b["native"] / b["json"], 4) for n, _, _, b in rows})
        + f" GEO {geo:.4f}"
    )


if __name__ == "__main__":
    main()
