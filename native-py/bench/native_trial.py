"""Run native_bench.py 3 times, keep the per-file minimum ratio, print geomean and per-file ratios as JSON."""

from __future__ import annotations

import json
import math
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
best: dict[str, float] = {}
for _ in range(3):
    out = subprocess.run(
        [sys.executable, str(HERE / "native_bench.py"), "25"],
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    line = next(x for x in out.splitlines() if x.startswith("RATIOS "))
    r = json.loads(line[7 : line.index(" GEO")])
    for k, v in r.items():
        best[k] = min(best.get(k, math.inf), v)
geo = math.exp(sum(map(math.log, best.values())) / len(best))
print(json.dumps({"geo": round(geo, 4), "files": best}))
