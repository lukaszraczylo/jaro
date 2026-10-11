#!/bin/sh
# Prints: <sum of min ns/op> <sum of min allocs/op> <sum of min B/op>
set -e
cd "$(dirname "$0")"
go test $TAGS -run '^$' -bench BenchmarkCodec -benchtime 300x -count 5 . 2>&1 | awk '
/^BenchmarkCodec/ { n=$1; ns=$3; by=$5; al=$7
  if (!(n in m) || ns<m[n]) m[n]=ns
  if (!(n in b) || by<b[n]) b[n]=by
  if (!(n in a) || al<a[n]) a[n]=al }
END { for (k in m) { s+=m[k]; t+=a[k]; u+=b[k] }; printf "%.0f %.0f %.0f\n", s, t, u }'
