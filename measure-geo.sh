#!/bin/sh
# Geometric mean of per-cell min ns/op over baseline (go-decode-baseline.json).
# SAVE_BASELINE=1 writes the baseline from this run. Prints GEO=<ratio> and per-cell ratios.
# REF_BIN=<test binary of the last kept state> runs it interleaved with the working
# tree and also prints REL (candidate/reference), which cancels machine-load drift.
set -e
cd "$(dirname "$0")"
: "${JARO_CORPUS:=$PWD/../corpus}"; export JARO_CORPUS
tmp=$(mktemp -d)
go test -c -o "$tmp/cand.test" .
benchA='^BenchmarkReal$/^(text_dec|bin_dec)_'
benchB='^BenchmarkRealArena$/^arena_'
bins="cand"; [ -n "$REF_BIN" ] && { cp "$REF_BIN" "$tmp/ref.test"; bins="ref cand"; }
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  for b in $bins; do
    for re in "$benchA" "$benchB"; do
      "$tmp/$b.test" -test.run '^$' -test.bench "$re" -test.benchtime 100x -test.count 1 2>&1 | sed "s/^/$b /" >>"$tmp/out"
    done
  done
done
python3 - "$tmp/out" <<'PY'
import sys, re, json, math, os
m = {}; ref = {}
for l in open(sys.argv[1]):
    p = l.split()
    if len(p) > 4 and p[1].startswith("Benchmark") and p[4] == "ns/op":
        n = re.sub(r"-\d+$", "", p[1]); v = float(p[3])
        t = ref if p[0] == "ref" else m
        t[n] = min(v, t.get(n, 1e30))
if len(m) != 18:
    print("BAD: cells =", len(m)); sys.exit(1)
bp = "go-decode-baseline.json"
if os.environ.get("SAVE_BASELINE") or not os.path.exists(bp):
    json.dump(m, open(bp, "w"), indent=1, sort_keys=True); print("baseline saved")
b = json.load(open(bp))
gm = lambda v: math.exp(sum(math.log(x) for x in v) / len(v))
r = {k: m[k] / b[k] for k in m}
rel = {k: m[k] / ref[k] for k in m} if ref else None
for k in sorted(r):
    print("%-50s %9.0f %.4f%s" % (k, m[k], r[k], " rel %.4f" % rel[k] if rel else ""))
for grp in ("text_dec", "bin_dec", "arena"):
    ks = [k for k in r if grp in k]
    print("  %-8s geo=%.4f%s" % (grp, gm([r[k] for k in ks]), " rel=%.4f" % gm([rel[k] for k in ks]) if rel else ""))
print("GEO=%.4f MAXCELL=%.4f" % (gm(r.values()), max(r.values())))
if rel:
    print("REL=%.4f MAXREL=%.4f" % (gm(rel.values()), max(rel.values())))
PY
rm -rf "$tmp"
