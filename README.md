# autocorrect

Reads one word per line on stdin, writes one corrected word per line on stdout.
Deterministic, no machine learning: a dictionary, word-frequency counts, and
hand-written spelling, keyboard and sound rules.

**Development set:** A = **0.9345** (1869/2000), T = **0.12 s**
(see [Evaluation](#evaluation)).

## Build and run

```sh
./build.sh                       # needs Go >= 1.25; produces ./autocorrect
./autocorrect < input.txt > output.txt
```

A prebuilt static Linux x86-64 binary can be made with
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o autocorrect .`.
The lexicon is embedded in the binary, so it needs no files at runtime.
`CODE_EXPLAINED.md` walks through `main.go` section by section.

## How it works (`main.go`)

1. **Lexicon.** SCOWL word lists up to size level 70 (165,436 words incl. names and
   contractions, accents folded to ASCII). A word found here is returned unchanged.
   Levels 80+ are excluded because they contain common misspellings (SCOWL 80 lists
   "expresso"). SCOWL's lowercase abbreviations and hacker jargon (etc, approx,
   blvd, grep) are also returned unchanged when typed, but never offered as
   corrections, so short misspellings are not pulled toward them.
2. **Prior** (how common a candidate is). `prior = 7.5 × ln(maxCount / count)`
   hundredths of an edit, with counts from wordfreq (subtitles, books, Wikipedia,
   news, web). Rarer SCOWL lists cost a little more (+1.2 × level / 100), and words
   SCOWL lists only as proper names cost +0.8 edits, since input is lowercase.
3. **Error model** (weighted Damerau-Levenshtein, costs in hundredths of an edit):
   - plain edit 100; vowel↔vowel 60; similar-sound letters (c/k/s, d/t, b/p, …) 60;
     adjacent QWERTY keys 75; doubled/undoubled letter 40; dropped/extra vowel or h 70;
     adjacent swap 70; apostrophe 60; +50 for an edit at the first letter;
     an extra key pressed next to the intended one 80.
   - ~75 sound-alike spelling rules, both directions (ph/f, ck/k, qu/kw,
     tion/sion/shun, ie/ei, ough/o, eau/o, kn/n, …).
   - Weak consonants dropped from clusters cost 40: n/m before a consonant
     (angina → agina), r/l after a consonant (scrub → scub) or after a vowel
     before a consonant (field → fied). This is a well-documented pattern in
     human spelling (Treiman 1993; Treiman, Zukowski & Richmond-Welty 1995).
4. **Sound keys.** A simplified Metaphone (`phonKey`, consonants only) and a
   vowel-aware key (`soundKey`, hand-written grapheme rules such as ee/ea → one
   long-e sound, so "wead" sounds like "weed" but not "wed"). A candidate whose key
   equals the input's gets 15 (`phonKey`) and 50 (`soundKey`) off.
5. **One edit first.** A candidate more than one ordinary edit away (insertion,
   deletion, substitution or adjacent swap) costs 60 more. Most misspellings are a
   single edit (Damerau 1964), so a single-edit candidate should usually win.
6. **Search.** Score = edit cost + prior + one-edit penalty − sound bonuses; lowest
   wins (ties: smaller edit, then earlier word). Every lexicon word is a candidate:
   a depth-first walk of a trie computes one DP row per node with exact
   branch-and-bound pruning (row minimum; earlier rows that an unfinished swap or
   sound rule can still jump from; smallest prior in the subtree). Words whose
   sound keys match the input are scored directly first, so the trie walk needs no
   bonus allowance and prunes tightly. If nothing is within edit cost 260 the word
   is kept as is.
7. **Speed.** Distinct words are corrected once, in parallel. Rules are matched
   as packed integers. Startup builds the trie and keys on all cores, with the
   garbage collector off (memory stays under 200 MB). Output does not depend on
   thread count. `search_test.go` checks the pruned search against an exhaustive
   search with no pruning.

Constants are at the top of `main.go`; `AC_<NAME>` environment variables
override them for tuning runs only.

## Data sources (cited)

- **SCOWL** 2020.12.07 (Spell Checker Oriented Word Lists), Kevin Atkinson,
  http://wordlist.aspell.net/: the dictionary. Notice: `data/SCOWL-Copyright`.
- **wordfreq** 3.1.1, Robyn Speer, https://github.com/rspeer/wordfreq
  (doi:10.5281/zenodo.7199437): word frequencies only (a stored frequency
  table, not a model). Notice and licenses: `data/WORDFREQ-NOTICE.md`,
  `data/wordfreq-LICENSE.txt`; the count column is CC BY-SA 4.0.
- **Birkbeck spelling error corpus** (missp, aspell, wikipedia, holbrook), Roger
  Mitton, https://www.dcs.bbk.ac.uk/~ROGER/corpora.html: **evaluation only**.
  The program never reads these files and contains no misspelling→correction pairs.
- Method references: Damerau (1964), *A technique for computer detection and
  correction of spelling errors*; Mitton (2009), *Ordering the suggestions of a
  spellchecker without using context*; Treiman et al. (1995), *What happened to the
  "n" of sink? Children's spellings of final consonant clusters*.

`tools/fetch_data.sh` downloads the data. `tools/build_lexicon.py` (needs
`pip install msgpack`) rebuilds `data/lexicon.tsv` byte-for-byte.

## Evaluation

**Development set** (2,000 words), scored with the organisers' `score.py`:

| | |
|---|---|
| A | **0.9345** (1869/2000): correct 99.75%, human 90.67%, keyboard 95.50% |
| T | **0.12 s** (10 cores; 0.42 s with GOMAXPROCS=1) |
| Peak memory | 167 MB |

Apple M4 (10 cores), macOS, Go 1.26. No constant was tuned on this set.

Tuning used local sets built by `tools/make_eval.py` (committed in `eval/`) from the
Birkbeck corpus, split by a hash of the correct word into **tune** (used to choose
the constants) and **holdout** (only for checking). They add correctly spelled words
and synthetic one-key slips. Constants were chosen on tune, mainly on non-word
misspellings one edit away, which is what the development set's human errors are.
Holdout confirmed each change. `python3 tools/score.py eval/holdout.tsv` reproduces
these numbers:

| Set | tune | holdout |
|---|---:|---:|
| Wikipedia misspellings | 83.9% | 86.1% |
| Aspell misspellings | 64.1% | 65.7% |
| Keyboard slips (synthetic) | 92.6% | 91.0% |
| Correct words kept | 100% / 99.7% | 100% / 99.6% |
| Birkbeck children's errors (missp) | 46.1% | 43.4% |

No rule refers to a specific word. On a 24,000-word input the program takes
0.85 s on 10 cores.
