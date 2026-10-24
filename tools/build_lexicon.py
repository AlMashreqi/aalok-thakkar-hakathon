#!/usr/bin/env python3
"""Build data/lexicon.tsv (word, scowl_level, count, w=word|n=name only|a=abbreviation)
from SCOWL + wordfreq. Abbreviation rows (a) are kept when typed but never
offered as corrections. Only SCOWL levels <= 70 are written (the program uses no more).

SCOWL decides what is a word; wordfreq only supplies how common it is.
Needs: pip install msgpack, and tools/fetch_data.sh run first."""
import gzip, os, re, sys, unicodedata

import msgpack

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RAW = os.path.join(ROOT, "data", "raw")
FINAL = os.path.join(RAW, "scowl-2020.12.07", "final")
MAX_LEVEL = 70
# wordfreq stores frequencies in centibel buckets; scale them to whole counts
WORDFREQ = os.path.join(RAW, "wordfreq-3.1.1", "large_en.msgpack.gz")
COUNT_SCALE = 5.88e11
VALID = re.compile(r"^[a-z]+(?:'[a-z]+)*$")
KINDS = ("words", "upper", "proper-names", "contractions")
# abbreviation lists: only entries written in lowercase (etc, approx), not acronyms
ABBR_KINDS = ("abbreviations", "hacker")
SPELLINGS = ("english", "american", "british", "canadian", "special")


def ascii_fold(s):
    s = unicodedata.normalize("NFKD", s)
    return "".join(c for c in s if not unicodedata.combining(c)).lower()


level = {}
abbr = {}  # abbreviation -> lowest SCOWL level
common = set()  # found in a lowercase word list, not only as a name
for name in sorted(os.listdir(FINAL)):
    m = re.match(r"^([a-z_]+)-([a-z-]+)\.(\d+)$", name)
    if not m or m.group(1) not in SPELLINGS or m.group(2) not in KINDS + ABBR_KINDS:
        continue
    lv = int(m.group(3))
    is_name = m.group(2) in ("upper", "proper-names")
    with open(os.path.join(FINAL, name), encoding="latin-1") as f:
        for line in f:
            raw = line.strip()
            w = ascii_fold(raw)
            if not VALID.match(w):
                continue
            if m.group(2) in ABBR_KINDS:
                if raw[:1].islower() and lv < abbr.get(w, 999):
                    abbr[w] = lv
                continue
            if w not in level or lv < level[w]:
                level[w] = lv
            if not is_name:
                common.add(w)

# bucket i holds words with frequency 10^(-i/100); a word's first bucket wins
freq = {}
with gzip.open(WORDFREQ) as f:
    buckets = msgpack.load(f, raw=False)
for i, bucket in enumerate(buckets[1:], 1):
    for w in bucket:
        freq.setdefault(w, 10 ** (-i / 100))

out = os.path.join(ROOT, "data", "lexicon.tsv")
with open(out, "w") as f:
    for w in sorted(level):
        if level[w] <= MAX_LEVEL:
            c = round(freq[w] * COUNT_SCALE) if w in freq else 0
            f.write(f"{w}\t{level[w]}\t{c}\t{'w' if w in common else 'n'}\n")
    for w in sorted(abbr):
        if abbr[w] <= MAX_LEVEL:
            f.write(f"{w}\t{abbr[w]}\t0\ta\n")
print(f"wrote {out}", file=sys.stderr)
