#!/bin/sh
# usage: tools/sweep.sh eval/tune-mini.tsv VAR "v1 v2 ..."  (other AC_* vars pass through)
for v in $3; do
  printf '%s=%s  ' "$2" "$v"
  env "AC_$2=$v" python3 tools/score.py "$1" | tr '\n' ' ' | sed 's/  */ /g'
  echo
done
