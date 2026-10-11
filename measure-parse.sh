#!/bin/sh
# Prints the sum of min ns/op of Parse over $JARO_CORPUS
set -e
cd "$(dirname "$0")"
: "${JARO_CORPUS:?set JARO_CORPUS to a directory of .json files}"
go test $TAGS -run '^$' -bench 'BenchmarkRealArena/arena_' -benchtime 100x -count 12 . 2>&1 | awk '
/^BenchmarkRealArena/ { n=$1; ns=$3; if (!(n in m) || ns<m[n]) m[n]=ns }
END { for (k in m) s+=m[k]; printf "%.0f\n", s }'
