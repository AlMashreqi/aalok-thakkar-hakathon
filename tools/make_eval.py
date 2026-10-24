#!/usr/bin/env python3
"""Build local evaluation sets (we were not given the official dev set here).

Sources: Birkbeck spelling error corpus files (missp, wikipedia, aspell, holbrook).
Pairs are split by a hash of the *correct* word into tune/holdout, so a parameter
tweak that only helps tune words shows up as a gap on holdout. Correct words are
mixed in (sampled by frequency from the lexicon) to measure overcorrection.
These files are only ever used for scoring, never read by the autocorrector."""
import hashlib, os, random, re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RAW = os.path.join(ROOT, "data", "raw")
OUT = os.path.join(ROOT, "eval")
VALID = re.compile(r"^[a-z]+(?:'[a-z]+)*$")
os.makedirs(OUT, exist_ok=True)


def pairs(path):
    cur = None
    for line in open(path, encoding="latin-1"):
        tok = line.split()
        if not tok:
            continue
        if tok[0].startswith("$"):
            cur = tok[0][1:].lower()
        elif cur:
            yield tok[0].lower(), cur


def split(word):
    return "holdout" if hashlib.md5(word.encode()).digest()[0] & 1 else "tune"


lex = {}
for line in open(os.path.join(ROOT, "data", "lexicon.tsv")):
    w, lv, c, _ = line.rstrip("\n").split("\t")
    lex[w] = (int(lv), int(c))
common = sorted((w for w, (lv, c) in lex.items() if lv <= 50 and c > 0), key=lambda w: -lex[w][1])[:30000]

sets = {"tune": [], "holdout": []}
for src in ("missp.dat", "wikipedia.dat", "aspell.dat", "holbrook-missp.dat"):
    seen = set()
    for typo, ref in pairs(os.path.join(RAW, src)):
        if typo != ref and VALID.match(typo) and VALID.match(ref) and ref in lex and (typo, ref) not in seen:
            seen.add((typo, ref))
            sets[split(ref)].append((typo, ref, src.split(".")[0].split("-")[0]))

KB = ["qwertyuiop", "asdfghjkl", "zxcvbnm"]
POS = {c: (r, i) for r, row in enumerate(KB) for i, c in enumerate(row)}
NEAR = {c: [d for d in POS if d != c and abs(POS[d][0] - r) <= 1 and abs(POS[d][1] + 0.5 * POS[d][0] - i - 0.5 * r) <= 1]
        for c, (r, i) in POS.items()}


def typo(w, rng):
    """One keyboard slip: drop, double, adjacent substitute/insert, or swap."""
    i = rng.randrange(len(w))
    op = rng.choice("dsitr")
    if op == "d":
        return w[:i] + w[i + 1:]
    if op == "s" and w[i] in NEAR:
        return w[:i] + rng.choice(NEAR[w[i]]) + w[i + 1:]
    if op == "i" and w[i] in NEAR:
        return w[:i] + rng.choice(NEAR[w[i]] + [w[i]]) + w[i:]
    if op == "t" and i + 1 < len(w) and w[i] != w[i + 1]:
        return w[:i] + w[i + 1] + w[i] + w[i + 2:]
    return w[:i] + w[i] + w[i:]


rng = random.Random(42)
for name in sets:
    pool = [w for w in common[:20000] if split(w) == name and len(w) >= 4 and "'" not in w]
    for w in rng.sample(pool, 4000):
        t = typo(w, rng)
        if t != w and t not in lex:
            sets[name].append((t, w, "typo"))

for name, rows in sets.items():
    rng.shuffle(rows)
    n_ok = len(rows) // 2  # roughly a third of the set is already-correct words
    # half common words, half the corpus' own target words (rarer, but real)
    pool = [w for w in common if split(w) == name]
    rows += [(w, w, "correct") for w in rng.sample(pool, n_ok // 2)]
    refs = sorted({r for _, r, src in rows if src not in ("correct", "typo")})
    rows += [(w, w, "correct-ref") for w in rng.sample(refs, min(len(refs), n_ok // 2))]
    rng.shuffle(rows)
    with open(os.path.join(OUT, name + ".tsv"), "w") as f:
        for r in rows:
            f.write("\t".join(r) + "\n")
    print(name, len(rows))
