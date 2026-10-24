# autocorrect

Reads one word per line on stdin, writes one corrected word per line on stdout.
Deterministic, no machine learning: a dictionary, word-frequency counts, and
hand-written spelling/keyboard/phonetic rules.

## Build and run

```sh
./build.sh                       # needs Go >= 1.25; produces ./autocorrect
./autocorrect < input.txt > output.txt
```

A prebuilt Linux x86-64 static binary is `autocorrect-linux-amd64` (rename to
`autocorrect`); rebuild it with `GOOS=linux GOARCH=amd64 go build -o autocorrect .`.
The lexicon is embedded in the binary, so it needs no files at runtime.

## How it works (`main.go`)

1. **Lexicon.** SCOWL word lists up to size level 70 (165,436 words incl. names and
   contractions, accents folded to ASCII). A word found here is returned unchanged.
   Levels 80+ are excluded because they contain common misspellings (e.g. SCOWL 80
   lists "expresso").
2. **Prior.** `prior = 9.0 * ln(maxCount / count)` in hundredths of an edit, with
   counts from Norvig's Google Web unigram list. That list contains misspellings too,
   so it only supplies frequencies, never decides what is a word. Contractions
   are looked up without their apostrophe ("don't" → "dont"), as that list was
   tokenized that way.
3. **Error model** (weighted Damerau-Levenshtein, costs in hundredths):
   plain edit 100; vowel↔vowel 60; similar-sound letters (c/k/s, d/t, b/p, …) 60;
   adjacent QWERTY keys 75; doubled/undoubled letter 40; dropped/extra vowel or h 70;
   adjacent swap 70; apostrophe 30; +50 for any edit at the first letter.
   Plus ~70 bidirectional grapheme rules for sound-alike spellings
   (ph/f, ck/k, qu/kw, tion/sion/shun, ie/ei, ough/o, eau/o, kn/n, …).
4. **Phonetic key.** A simplified Metaphone (`phonKey`); a candidate whose key equals
   the input's key gets 30 off. This helps spell-by-sound errors.
5. **Search.** Score = edit cost + prior − (30 if phonetic keys match); lowest wins
   (ties: smaller edit, then earlier word). Every lexicon word is a candidate through a depth-first walk of a
   trie, computing one DP row per node, with exact branch-and-bound pruning: the
   row minimum, earlier rows that an unfinished swap or grapheme rule can still
   jump from, and the smallest prior in the subtree. Children are visited most
   frequent first, and a cheap first pass (edit ≤ 140) seeds the bound. If nothing
   is within edit cost 260 the word is kept as is.
6. Distinct words are corrected once, in parallel. The output order and result
   don't depend on thread count, which is verified by tests.

Constants are at the top of `main.go`; `AC_<NAME>` environment variables
override them for tuning runs only.

## Data sources (cited)

- **SCOWL** 2020.12.07 (Spell Checker Oriented Word Lists), Kevin Atkinson,
  http://wordlist.aspell.net/: dictionary. Its copyright notice is in `data/SCOWL-Copyright`.
- **count_1w.txt**, Peter Norvig, derived from the Google Web Trillion Word Corpus
  (Brants & Franz 2006), https://norvig.com/ngrams/: word frequencies.
- **Birkbeck spelling error corpus** (missp, aspell, wikipedia, holbrook), Roger
  Mitton, https://www.dcs.bbk.ac.uk/~ROGER/corpora.html: **evaluation only**.
  The program never reads these files.

`tools/fetch_data.sh` downloads them. `tools/build_lexicon.py` builds
`data/lexicon.tsv` (word, SCOWL level, count, word/name flag).

## Evaluation

The public development set was not available to us. We evaluated on local sets
built by `tools/make_eval.py` from the Birkbeck corpus. Pairs are split by a hash
of the correct word into **tune** (used to choose the few constants) and
**holdout** (never tuned on). Correctly spelled words are mixed in, and synthetic
single keyboard slips are added. `python3 tools/score.py eval/holdout.tsv` reproduces
these numbers.

| Set | tune | holdout |
|---|---:|---:|
| Wikipedia misspellings | 83.0% | 85.7% |
| Aspell misspellings | 63.7% | 65.7% |
| Keyboard slips (synthetic) | 89.8% | 88.5% |
| Correct words kept | 100% / 99.7% | 100% / 99.6% |
| Birkbeck children's errors (missp) | 44.9% | 42.6% |

The few scalar constants were chosen on the tune split. Holdout scores match tune scores, which suggests the
constants generalize. No rule refers to a specific word.

Time: about 0.2 s startup, then about 1 ms of CPU per misspelled word. Example
workload: 10,000 distinct misspellings of common words (one random edit each).
That takes 1.5 s with 10 cores and 7.4 s with GOMAXPROCS=1, on an Apple M-series
Mac under macOS. Peak memory is about 85 MB.

Development set (fill in when run): A = ___, T = ___ s.
