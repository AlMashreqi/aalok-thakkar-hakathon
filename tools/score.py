#!/usr/bin/env python3
"""Run ./autocorrect on an eval TSV (typo, ref, source) and report accuracy by source."""
import collections, subprocess, sys, time

path = sys.argv[1]
binary = sys.argv[2] if len(sys.argv) > 2 else "./autocorrect"
rows = [l.rstrip("\n").split("\t") for l in open(path)]
t0 = time.time()
r = subprocess.run([binary], input="".join(t + "\n" for t, _, _ in rows), capture_output=True, text=True, check=True)
dt = time.time() - t0
out = r.stdout.split("\n")[:-1]
assert len(out) == len(rows), (len(out), len(rows))
tot, ok = collections.Counter(), collections.Counter()
for (t, ref, src), c in zip(rows, out):
    tot[src] += 1
    ok[src] += c.strip() == ref
    if "-v" in sys.argv and c.strip() != ref:
        print(f"{src}\t{t}\t{ref}\t{c}")
for src in sorted(tot):
    print(f"{src:10s} {ok[src] / tot[src]:.4f}  ({tot[src]})")
print(f"{'ALL':10s} {sum(ok.values()) / len(rows):.4f}  time {dt:.2f}s")
