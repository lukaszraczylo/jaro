#!/bin/sh
# Prints: <sum of min ns/op> <sum of min allocs/op> <sum of min B/op> over $JARO_CORPUS
set -e
cd "$(dirname "$0")"
: "${JARO_CORPUS:?set JARO_CORPUS to a directory of .json files}"
go test $TAGS -run '^$' -bench '^BenchmarkReal$' -benchtime 100x -count 12 . 2>&1 | awk '
/^BenchmarkReal\// { n=$1; ns=$3; by=$5; al=$7
  if (!(n in m) || ns<m[n]) m[n]=ns
  if (!(n in b) || by<b[n]) b[n]=by
  if (!(n in a) || al<a[n]) a[n]=al }
END { for (k in m) { s+=m[k]; t+=a[k]; u+=b[k]; if (ENVIRON["VERBOSE"]) printf "%s %.0f\n", k, m[k] > "/dev/stderr" }; printf "%.0f %.0f %.0f\n", s, t, u }'
