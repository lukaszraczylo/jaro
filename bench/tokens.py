import collections
import glob
import os
import sys

import tiktoken

enc = tiktoken.get_encoding("o200k_base")
rows: dict = collections.defaultdict(dict)
for p in sorted(glob.glob(os.path.join(sys.argv[1], "*"))):
    ds, fmt = os.path.basename(p).rsplit(".", 1)
    if fmt.startswith("jarob"):
        continue
    rows[ds][fmt] = len(enc.encode(open(p, encoding="utf-8").read()))
for ds, d in rows.items():
    j = d["json"]
    print(ds, " ".join(f"{k}={v}({v/j:.0%})" for k, v in d.items()))
