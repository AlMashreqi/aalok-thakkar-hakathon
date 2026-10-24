#!/usr/bin/env python3
"""Build data/lexicon.tsv (word, scowl_level, google_count, w=word|n=name only) from SCOWL + Norvig count_1w.

SCOWL decides what is a word; count_1w only supplies frequencies (it also contains
common misspellings, so it is never used to decide validity)."""
import os, re, sys, unicodedata

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RAW = os.path.join(ROOT, "data", "raw")
FINAL = os.path.join(RAW, "scowl-2020.12.07", "final")
VALID = re.compile(r"^[a-z]+(?:'[a-z]+)*$")
# word lists (no abbreviations: lowercased acronyms would shadow real corrections)
KINDS = ("words", "upper", "proper-names", "contractions")
SPELLINGS = ("english", "american", "british", "canadian")


def ascii_fold(s):
    s = unicodedata.normalize("NFKD", s)
    return "".join(c for c in s if not unicodedata.combining(c)).lower()


level = {}
common = set()  # found in a lowercase word list, not only as a name
for name in sorted(os.listdir(FINAL)):
    m = re.match(r"^([a-z_]+)-([a-z-]+)\.(\d+)$", name)
    if not m or m.group(1) not in SPELLINGS or m.group(2) not in KINDS:
        continue
    lv = int(m.group(3))
    is_name = m.group(2) in ("upper", "proper-names")
    with open(os.path.join(FINAL, name), encoding="latin-1") as f:
        for line in f:
            w = ascii_fold(line.strip())
            if not VALID.match(w):
                continue
            if w not in level or lv < level[w]:
                level[w] = lv
            if not is_name:
                common.add(w)

count = {}
with open(os.path.join(RAW, "count_1w.txt")) as f:
    for line in f:
        w, c = line.split("\t")
        count[w] = int(c)

out = os.path.join(ROOT, "data", "lexicon.tsv")
with open(out, "w") as f:
    for w in sorted(level):
        # count_1w was tokenized without apostrophes, so "don't" is counted as "dont"
        c = count.get(w) or count.get(w.replace("'", ""), 0)
        f.write(f"{w}\t{level[w]}\t{c}\t{'w' if w in common else 'n'}\n")
print(f"wrote {len(level)} words to {out}", file=sys.stderr)
