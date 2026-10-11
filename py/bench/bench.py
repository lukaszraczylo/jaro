"""Time JARO against json and PyYAML on a directory of real JSON files.

Usage: python bench/bench.py [--budget SECONDS] [--yaml auto|c|pure]
The corpus is $JARO_CORPUS, default ../../corpus relative to py/.
Writes bench/results.json as {file: {format: {enc_ms, dec_ms, bytes}}}.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

PY_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(PY_ROOT / "src"))
sys.path.insert(0, str(PY_ROOT / "tests"))

from conftest import same  # noqa: E402

import jaro  # noqa: E402

MIN_RUNS = 3
MAX_RUNS = 200


def best_ms(fn: Callable[[], Any], budget: float) -> float:
    """Return the fastest run in ms: at least MIN_RUNS runs, then stop after budget seconds."""
    best = float("inf")
    start = time.perf_counter()
    runs = 0
    while runs < MIN_RUNS or (runs < MAX_RUNS and time.perf_counter() - start < budget):
        t0 = time.perf_counter()
        fn()
        best = min(best, time.perf_counter() - t0)
        runs += 1
    return best * 1000


def yaml_codec(
    choice: str,
) -> tuple[str, Callable[[bytes], Any], Callable[[Any], str]] | None:
    try:
        import yaml  # type: ignore[import-untyped,unused-ignore]
    except ImportError:
        return None
    c_ok = bool(getattr(yaml, "__with_libyaml__", False))
    if choice == "c" and not c_ok:
        raise SystemExit("libyaml is not available in this PyYAML build")
    if (choice in ("auto", "c")) and c_ok:
        return (
            "PyYAML CSafeLoader/CSafeDumper (libyaml)",
            lambda b: yaml.load(b, Loader=yaml.CSafeLoader),
            lambda v: yaml.dump(v, Dumper=yaml.CSafeDumper, allow_unicode=True),
        )
    return (
        "PyYAML safe_load/safe_dump (pure Python)",
        yaml.safe_load,
        lambda v: yaml.safe_dump(v, allow_unicode=True),
    )


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--budget",
        type=float,
        default=2.0,
        help="seconds per cell after the first 3 runs",
    )
    ap.add_argument("--yaml", choices=("auto", "c", "pure"), default="auto")
    args = ap.parse_args()
    corpus = Path(
        os.environ.get("JARO_CORPUS") or PY_ROOT / ".." / ".." / "corpus"
    ).resolve()
    files = sorted(corpus.glob("*.json"))
    if not files:
        raise SystemExit(f"no .json files in {corpus}")
    ycodec = yaml_codec(args.yaml)
    print(f"corpus: {corpus}")
    print(
        f"yaml:   {ycodec[0] if ycodec else 'not installed (pip install pyyaml); YAML skipped'}"
    )
    print(
        f"python: {sys.version.split()[0]}  budget: {args.budget}s/cell, min {MIN_RUNS} runs, min of runs reported\n"
    )

    results: dict[str, dict[str, dict[str, float | int]]] = {}
    for path in files:
        raw = path.read_bytes()
        tree = json.loads(raw)
        text = jaro.dumps(tree)
        blob = jaro.dumps_binary(tree)
        if not same(jaro.loads(text), tree) or not same(jaro.loads_binary(blob), tree):
            raise SystemExit(f"{path.name}: round trip changed the tree")
        text_b = text.encode("utf-8")
        compact = json.dumps(tree, separators=(",", ":"), ensure_ascii=False)
        cells: dict[str, tuple[Callable[[], Any], Callable[[], Any], int]] = {
            "json": (
                lambda: json.dumps(tree, separators=(",", ":"), ensure_ascii=False),
                lambda: json.loads(raw),
                len(compact.encode("utf-8")),
            ),
            "jaro": (lambda: jaro.dumps(tree), lambda: jaro.loads(text_b), len(text_b)),
            "jaro-bin": (
                lambda: jaro.dumps_binary(tree),
                lambda: jaro.loads_binary(blob),
                len(blob),
            ),
        }
        if ycodec is not None:
            ytext = ycodec[2](tree).encode("utf-8")
            cells["yaml"] = (
                lambda: ycodec[2](tree),
                lambda: ycodec[1](ytext),
                len(ytext),
            )
        out: dict[str, dict[str, float | int]] = {}
        print(f"{path.name}  ({len(raw):,} B source)")
        print(f"  {'format':<9}{'enc ms':>11}{'dec ms':>11}{'bytes':>12}{'vs json':>9}")
        for name in ("json", "yaml", "jaro", "jaro-bin"):
            if name not in cells:
                continue
            enc, dec, size = cells[name]
            out[name] = {
                "enc_ms": round(best_ms(enc, args.budget), 4),
                "dec_ms": round(best_ms(dec, args.budget), 4),
                "bytes": size,
            }
            ratio = size / cells["json"][2]
            print(
                f"  {name:<9}{out[name]['enc_ms']:>11.3f}{out[name]['dec_ms']:>11.3f}{size:>12,}{ratio:>8.0%}"
            )
        results[path.name] = out
        print()
    (PY_ROOT / "bench" / "results.json").write_text(
        json.dumps(results, indent=2) + "\n", "utf-8"
    )
    print(f"wrote {PY_ROOT / 'bench' / 'results.json'}")


if __name__ == "__main__":
    main()
