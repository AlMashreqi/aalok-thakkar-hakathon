# How `autocorrect` works: a section-by-section guide

This guide walks through [`main.go`](main.go) from top to bottom. Every section has two parts:

- **In simple words**: what the code does, explained with no jargon.
- **Technically**: how it does it, why, and the exact numbers.

All the numbers in the examples come from the real program running on the real lexicon.

---

## 0. The big picture

**In simple words.** You type a word. If it's in the dictionary, you get it back unchanged. If it isn't, the program gives every dictionary word a **cost**, meaning how unlikely it is that you meant that word, and returns the cheapest one.

```
typed word ──► in dictionary (or a known abbreviation)? ──yes──► return it unchanged
                    │ no
                    ▼
     score every dictionary word:
     score = edit cost + prior + one-edit penalty − sound bonuses
                    │
                    ▼
     return the lowest score (or the word itself if nothing is close)
```

**The parts of the score**

| Part | Meaning | Example |
|---|---|---|
| **edit cost** | How much typing damage turns the dictionary word into what you typed? | `the` → `teh` is one swap = 70 |
| **prior** | How rare is the dictionary word? Rare words cost more. | `the` = 12, `ten` = 58 |
| **one-edit penalty** | 60 extra if the word is more than one plain edit away | `agina` → `again` (2 edits) |
| **sound bonuses** | 15 off if the consonants sound alike, 50 more off if the vowels do too | `wead` and `weed` both sound like "WID" |

All costs are in **hundredths of an edit**: 100 means one ordinary typo.

**Worked example: you typed `teh`**

| Candidate | Edit | Prior | Total |
|---|---:|---:|---:|
| **the** | 70 (swap e/h) | 12 | **82** ✅ |
| ten | 75 (h→n, neighbouring keys) | 58 | 133 |
| tea | 100 (h→a) | 64 | 149 − 15 (sounds like "t") |

**Worked example: you typed `wead`**

| Candidate | Edit | Prior | Bonuses | Total |
|---|---:|---:|---:|---:|
| **weed** | 40 (`ea` for `ee`, a sound-alike rule) | 102 | −15 −50 | **77** ✅ |
| wed | 50 (`ea` for `e`) | 94 | −15 | 129 |
| read | 150 | 50 | 0 | 200 |

`wed` is more common, but `weed` *sounds* exactly like `wead`, so it wins.

**Technically.** This is the classic noisy-channel model of spelling correction: pick the word *w* that maximises P(w) × P(typed | w). Taking −log turns the product into a sum of costs. The **prior** is −log P(w), and the **edit cost** stands in for −log P(typed | w). Every cost is hand-written, not learned, so there is no machine learning and the output is fully deterministic.

---

## 1. Header, imports, embedded data

**In simple words.** The dictionary file is baked into the program, so the binary needs no other files to run.

**Technically.**

- `//go:embed data/lexicon.tsv` puts the whole TSV into the string `lexiconTSV` at compile time.
- `lexicon.tsv` has 4 columns: `word  scowl_level  count  flag`. The flag is `w` (word), `n` (name only) or `a` (abbreviation). Only SCOWL levels ≤ 70 are stored.
- `tools/build_lexicon.py` builds this file (see §16).

---

## 2. Tunable constants

**In simple words.** These are the knobs. Each knob has a default, and you can change it without recompiling by setting an environment variable, e.g. `AC_PHONE=20 ./autocorrect`.

**Technically.** `envInt(name, def)` reads `AC_<NAME>` and falls back to the default.

| Constant | Default | What it controls |
|---|---:|---|
| `maxLevel` | 70 | Biggest SCOWL list accepted as "real words". Level 80+ includes misspellings like "expresso". |
| `priorScale` | 75 | Prior = 7.5 × ln(frequency ratio) |
| `zeroCount` | 500 | Pretend count for a dictionary word wordfreq has no count for |
| `maxEdit` | 260 | Candidates with edit cost above this are never considered |
| `firstExtra` | 50 | Extra cost for an edit at the first letter (people rarely get the first letter wrong) |
| `firstPass` | 140 | Edit limit of a quick first search (see §13) |
| `phoneBonus` | 15 | Discount when the consonant sound key matches |
| `soundBonus` | 50 | Extra discount when the vowel-aware sound key matches too |
| `clusterCost` | 40 | Dropping a weak consonant from a cluster (`agina` for `angina`) |
| `aposCost` | 60 | Missing or extra apostrophe (`dont` ↔ `don't`) |
| `namePen` | 80 | Extra prior for words that are only names (input is lowercase, so it usually means a common word) |
| `levelPen` | 120 | Extra prior per 100 SCOWL levels (+0.84 edits at level 70) |
| `multiPen` | 60 | Extra cost for a candidate more than one plain edit away |

Only tuning runs use the environment overrides. The defaults are what ships.

---

## 3. Data structures

**In simple words.** The dictionary is stored as a **trie**: a tree where words that start the same share a path. `the`, `then`, `them` and `there` all share the path t → h → e.

```
(root)
  └─ t
     └─ h
        └─ e  ← "the" ends here
           ├─ n  ← "then"
           ├─ m  ← "them"
           └─ r
              └─ e  ← "there"
```

**Technically.**

```go
type node struct {
    ch          byte   // the letter on the edge into this node
    word        int32  // index into words[] if a word ends here, else -1
    minPrior    int32  // cheapest prior of any word in this subtree (used for pruning)
    first, next int32  // first child, next sibling: a linked list stored inside a slice
}
```

- All nodes live in one flat slice `trie []node` and point to each other with `int32` indexes, not pointers. That is compact (391,671 nodes) and cache-friendly.
- Per-word data sits in parallel slices indexed by word number: `words[i]` (spelling), `prior[i]` (rarity cost), `phon[i]` (consonant sound key), `snd[i]` (vowel-aware sound key).
- `byPhon` and `bySnd` map a sound key to the words that have it, so the words that can get a sound bonus are found instantly (§13).
- `abbr` is a set of abbreviations (`etc`, `approx`, `grep`). They are kept when typed, but never suggested.

---

## 4. `load()`: building the dictionary

**In simple words.** Read every line of the lexicon, put each word into the trie, work out how rare it is, and compute its sound keys.

**Technically, step by step:**

1. **Filter.** Rows flagged `a` go into `abbr` and are **not** inserted into the trie, so they can never win a search.
2. **Missing counts.** If the count is 0, use `zeroCount` (500).
3. **Insert** each word into the trie by walking down with `child()` letter by letter, creating nodes as needed.
4. **Prior** (the core formula):

   ```
   prior = round(7.5 × (ln maxCount − ln count))  +  120 × level / 100  +  (80 if name-only)
   ```

   - `count`: how often the word appears in wordfreq's English data (subtitles, books, Wikipedia, news, web)
   - `maxCount`: the count of the most common word ("the")
   - `ln(maxCount / count)`: how many times rarer than "the", on a log scale

   | Word | Prior |
   |---|---:|
   | the | 12 |
   | then | 41 |
   | phone | 54 |
   | ten | 58 |
   | july | 86 |
   | jolly | 96 |
   | angina | 146 |

   Why a log? Frequencies span 9 orders of magnitude. The log turns "100× rarer" into a fixed extra cost (7.5 × ln 100 ≈ 35, about a third of an edit) rather than an enormous one.
5. **Sound keys** (§11) for all words are computed on every CPU core at once, since each word's keys are independent.
6. `setMinPrior(0)` finishes the trie (§5), and `buildKeyIndex()` fills `byPhon` / `bySnd`.

Result: 165,436 words and 2,222 abbreviations, built in about 0.05 s.

---

## 5. `setMinPrior()`: preparing the trie for fast search

**In simple words.** Every node remembers the most common word below it. Children are reordered so the branch with the most common words is explored first.

**Technically.**

- A recursive post-order pass. `minPrior(node) = min(own word's prior, minPrior of every child)`.
- That value is a **lower bound**: no word in this subtree can have a lower prior. The search uses it to skip whole subtrees (§14).
- It then re-links the children, sorted by `minPrior` ascending (stable sort, so ties stay alphabetical). Visiting likely words first finds a good answer early, which tightens the bound sooner and prunes more of the rest.

---

## 6. `child()` and `lookup()`

**In simple words.** `child` = "go to the next letter, creating it if needed". `lookup` = "is this exact word in the dictionary?"

**Technically.**

- `child(n, ch)` walks the sibling list, which is kept sorted by letter while building, and inserts a new node in place if `ch` isn't there yet.
- `lookup(w)` follows one letter at a time and succeeds only if the last node has `word >= 0`. Abbreviations aren't in the trie, so `correct()` checks `abbr` separately.

---

## 7. The error model: how much a typo costs

**In simple words.** Some mistakes are more likely than others. Hitting the key next door is more likely than a random wrong letter. Mixing up two vowels is more likely than mixing up two consonants. The table `sub` writes this down.

**Technically.** `sub[a][b]` is the cost when the intended letter `a` was typed as `b`. It is a 256×256 table filled in this order, each step overwriting the last:

| Step | Rule | Cost |
|---|---|---:|
| 1 | Any substitution | 100 |
| 2 | QWERTY neighbours | 75 |
| 3 | Vowel ↔ vowel (`a e i o u y`) | 60 |
| 4 | Sound-alike letters: `c/k c/s s/z k/q g/j f/v d/t b/p m/n s/c x/z x/s i/y e/y w/u` | 60 |
| 5 | Same letter | 0 |

**Keyboard neighbours.** Each key gets an x position of `column + 0.5 × row`, because rows are offset by about half a key. Two keys are neighbours if they are at most one row apart and their x positions differ by at most 1. A separate table `near[a][b]` records keyboard neighbours on their own, because step 3 or 4 can overwrite the 75 in `sub` and the "extra key" rule (§10) still needs to know which keys are adjacent.

Example: `h` is row 1, column 5 → x = 5.5. `n` is row 2, column 5 → x = 6.0. They are 0.5 apart, so they're neighbours and cost 75.

---

## 8. Grapheme rules: sound-alike spellings and dropped consonants

**In simple words.** People spell by sound: `fone` for `phone`, `nashun` for `nation`. These rules replace a whole **chunk** of letters at once, at a discount, so `ph → f` costs 40 instead of "delete p + substitute h→f" (≈ 170).

People also tend to leave out a "weak" consonant inside a cluster of consonants: `agina` for `angina`, `scub` for `scrub`, `fied` for `field`. Studies of children's spelling (Treiman) document exactly this. Cluster rules make those omissions cost 40 instead of 100.

**Technically.**

- About 75 sound-alike pairs like `{"ph","f",40}`, `{"tion","shun",40}`, `{"ough","o",60}`, each added **in both directions**.
- **Cluster rules** (one direction only: the dictionary has the cluster, the typo lacks the letter), cost `clusterCost` = 40:
  - `n`/`m` before any consonant: `nt → t`, `mp → p`, …
  - `r`/`l` after any consonant: `cr → c`, `bl → b`, …
  - vowel + `r`/`l` + consonant: `ield → ied`, `alf → af`, …
- 416 rules in total. A `rule` holds `typo` (what was typed), `dict` (what the dictionary has) and `cost`, plus `dk`/`mask`: the dict chunk packed into one integer so the search can compare it with a single `==` (§9, §14).
- `maxRuleDict` = 5, the longest chunk (`cious`).
- **`rulePrefix`** is a lookup table for pruning, not for scoring. For every *proper prefix* of a rule's dictionary chunk (e.g. `t`, `ti`, `tio` for `tion`), it stores the cheapest rule that starts that way. §14 explains why the search needs it.

---

## 9. `pack()` and `code`

**In simple words.** This turns a short string into one number, so lookups and comparisons are a single integer operation instead of a string comparison.

**Technically.**

- `code` maps `a..z` → 1..26 and `'` → 27, which fits in 5 bits.
- `pack("ti") = (20 << 5) | 9`, with the first letter in the highest bits. Prefixes are at most 4 letters, so `rulePrefix` has 2²⁰ ≈ 1 M `int32` entries, about 4 MB.
- The search keeps the current trie path packed the same way (`pk[j]`, up to 12 letters in 64 bits). A rule matches when `pk[j] & rule.mask == rule.dk`.

---

## 10. `omitCost()` and `insCost()`: missing and extra letters

**In simple words.** A dropped letter or an extra letter is an edit too, but some are cheap:

| Situation | Example | Cost |
|---|---|---:|
| Doubled letter typed once / single letter typed twice | `ocurred`, `untill` | 40 |
| Apostrophe missing or extra | `dont` | 60 |
| Vowel or `h` dropped / added | `definatly`, `whith` | 70 |
| Any other letter | | 100 |
| Extra letter next to the right key (fat finger) | `thre` (r is next to e) | 80 |
| Anything at the first letter | | +50 |

**Technically.**

- `omitCost(path, j)`: the dictionary letter `path[j]` was not typed. It's "doubled" if `path[j-1]` is the same letter.
- `insCost(t, i)` returns two costs for an extra typed letter `t[i-1]`:
  - the normal cost
  - `insFat` = 80, used in §14 when the extra letter is a keyboard neighbour of the dictionary letter at that point, or of the letter typed right after it
- Both are computed once per typed word and cached in `s.ins` / `s.fat`.

---

## 11. `phonKey()` and `soundKey()`: what a word sounds like

**In simple words.** Both turn a word into a rough "sound code", so words that sound alike get the same code. `phonKey` only keeps consonants. `soundKey` keeps the vowel sounds too, so it can tell `weed` from `wed`.

| Word | `phonKey` | `soundKey` |
|---|---|---|
| knight / nite | `nt` / `nt` | `NYT` / `NYT` |
| phone / fone | `fn` / `fn` | `FON` / `FON` |
| wead / weed / wed | `wd` / `wd` / `wd` | `WID` / `WID` / `WeD` |
| joly / jolly / july | `jl` / `jl` / `jl` | `JoLi` / `JoLi` / `JuLi` |

**Technically.**

- **`phonKey`** is a simplified **Metaphone**: silent starts (`kn gn ps wr`) drop a letter; vowels are dropped except at the start; `ch`/`sh`/`tio` → `x`; `ph` → `f`; `th` → `0`; soft `c`/`g`; `q k` → `k`, `z` → `s`, `v` → `f`; silent `h`/`w`/`tch`/final `mb`; repeats collapse.
- **`soundKey`** applies hand-written grapheme rules, longest first: `tion/sion/cian` → `SHN`, `igh` → `Y` (long i), `ee/ea/ei` → `I` (long e), `oa/oe/final ow` → `O`, `oo/ew/ue` → `U`, `ai/ay` → `A`, `ar/er/ir/ur/or`, `ch/sh/th`, and so on. A vowel followed by one consonant and a final `e` is long (`fone` → `FON`); otherwise vowels stay short (lowercase). Silent final `e` is dropped and repeats collapse.
- A candidate gets `phoneBonus` (15) if its `phonKey` equals the input's, and `soundBonus` (50) more if its `soundKey` does. Both keys are computed once per dictionary word at startup.

---

## 12. The `searcher` struct

**In simple words.** This is a reusable scratch pad. Each worker thread has its own, so threads never step on each other.

**Technically.**

| Field | Purpose |
|---|---|
| `t` | The typed word as bytes |
| `rows[j]` | DP row for depth `j` in the trie (one row per letter of the current path) |
| `rowMin[j]` | Smallest value in `rows[j]` |
| `ins`, `fat` | Cached insertion costs per typed position |
| `path`, `pk` | Letters from the root to the current node, and the same packed into integers |
| `rmatch[i][c]` | Rules whose **typed** chunk ends at typed position `i` and whose dictionary chunk ends in letter code `c` (worked out once per word) |
| `bound` | Current edit-cost limit; anything above it is pruned |
| `best`, `bestEdit`, `bestWord` | The best candidate found so far |
| `key`, `skey` | The input's two sound keys |
| `allow` | The largest bonus a not-yet-scored word could still get (0 once bonus words are scored directly) |

Rows are allocated once with a fixed size (`maxDepth` = 64). Words of 64+ characters are returned unchanged.

---

## 13. `correct()`: correcting one word

**In simple words.**

1. Known word or abbreviation? Return it.
2. Prepare the scratch pad.
3. Score the few dictionary words that **sound like** the input first.
4. Search the trie twice: a quick search that only allows small edits, then the full search.
5. Nothing found within the limit? Return the word unchanged.

**Technically.**

- **`rmatch`.** For each typed position `i`, it lists the rules whose `typo` chunk ends exactly there, grouped by the last letter of their `dict` chunk. For `fone`, the rule `f → ph` matches at `i = 1`.
- **Row 0** (the empty dictionary prefix) is `row[i] = row[i-1] + ins[i]`: "every typed letter so far is extra".
- **Bonus words first.** Only words whose keys equal the input's (`byPhon[key]`, `bySnd[skey]`) can get a bonus. If there are at most 400, each is scored directly with `scoreWord`. After that, every word the trie search still meets gets no bonus, so `allow` = 0 and pruning (§14) is much tighter. With more than 400 such words, `allow` stays 65 and the trie search handles them.
- **Two passes:**
  - Pass 1 uses bound = 140.
  - Pass 2 uses bound = min(260, best + allow), keeping pass 1's best as the starting answer.
  - Because pruning is exact (§14), the answer is the **same** as one full search with no pruning. `search_test.go` checks this against an exhaustive search.

---

## 14. `step()`, `consider()`, `scoreWord()`, `dfs()`: the search engine

This is the heart of the program.

### 14a. `step()`: one DP row per trie node

**In simple words.** The standard way to measure "how different are two words" (edit distance) fills a grid: typed letters across the top, dictionary letters down the side. Each cell = the cheapest way to turn this much of the dictionary word into this much of what was typed.

The trick: walking the trie adds **one dictionary letter at a time**, so each trie node only needs to compute **one new row**, built from its parent's row. `the`, `then`, `there` and `them` share the rows for `t`, `th` and `the`, so that work is done once, not four times.

**Worked example: you typed `teh`, and the search walks t → h → e:**

```
                     typed →   ""    t     e     h
dictionary ↓
  ""   (root)                   0   150   220   290
  "t"                         150     0    70   140
  "th"                        220    70   100    70
  "the"                       290   140    70    70  ◄ last cell = edit cost of "the"
```

- **`"t"` row, `t` column = 0**: t matches t, at no cost.
- **`"t"` row, `e` column = 70**: an extra typed `e` (a vowel) costs 70.
- **`""` row, `t` column = 150**: an extra letter (100) at the first position (+50).
- **`"the"` row, `h` column = 70**: the **swap** rule. The dictionary has `h e` where `e h` was typed. It jumps from the cell two rows and two columns back (`"t"` row, `t` column = 0) and adds 70.

**Technically, each cell `row[i]` takes the minimum of these moves:**

| Move | Formula | Meaning |
|---|---|---|
| substitute / match | `prev[i-1] + sub[d][x]` (+50 if first letter) | dictionary letter `d` typed as `x` |
| omit | `prev[i] + omitCost` | dictionary letter not typed |
| insert | `row[i-1] + ins` (or `fat` = 80 if `x` is next to `d` on the keyboard) | extra typed letter |
| stray key before | `prev[i-2] + fat` | an extra key pressed just before its neighbour |
| transpose | `rows[j-2][i-2] + 70` | two neighbouring letters swapped |
| grapheme rule | `rows[j-len(dict)][i-len(typo)] + cost` | sound-alike chunk or dropped cluster consonant |

This is **weighted Damerau–Levenshtein distance** (edit distance that also counts swaps, with a different cost per edit), extended with multi-letter rules. Only rules in `rmatch[i][code of d]` are tried, and each is checked with one integer comparison against the packed path.

### 14b. `consider()`: scoring a word

```
total = edit + prior[w]
      + 60 if w is more than one plain edit from the input   (withinOne)
      − 15 if phon[w] == key
      − 50 if snd[w] == skey
```

If that beats the best so far, it becomes the new best. Ties go to the smaller edit, then the lower word index, so the answer never depends on visiting order or thread timing. It then tightens `bound = min(bound, total + allow)`: priors and the penalty are ≥ 0, so a word with a larger edit cost than that can't win.

`withinOne(a, b)` checks in one linear pass whether two strings differ by one insertion, deletion, substitution or adjacent swap.

`scoreWord()` scores one given word by walking its own path in the trie with `step()`, which is how the bonus words in §13 are scored directly.

### 14c. `dfs()`: pruning whole branches

**In simple words.** If every cell in this row is already too expensive, adding more letters can only make it worse, so skip everything below this node. That one check means most of the 391k nodes are never visited.

Example: after finding `the` (total 82), the bound drops to 82 plus any remaining bonus allowance, at most 147. The row for prefix `q` has a minimum of 150, which is over that, so **no word starting with `q` is ever looked at**.

**Technically.** `lo` is a lower bound on any cell in any row below this node. It is the row minimum, lowered by two kinds of **look-back**:

- **Swaps** can jump from two rows back: `rowMin[j-1] + 70`.
- **Grapheme rules** that have started but not finished can jump from `k` rows back. If the last `k` letters of the path are a proper prefix of a rule's dictionary chunk (`rulePrefix[...]`), that rule might complete deeper and jump from `rowMin[j-k] + cost`.

Without these look-backs, pruning could wrongly cut a branch where a swap or rule would have produced a cheap match. With them, pruning is **exact**: it never changes the answer.

The branch is cut if either:

```
lo > bound                                   // edit cost alone already too high
lo + minPrior[node] − allow > best           // even the most common word below can't win
```

---

## 15. `main()`: input, parallelism, output

**In simple words.** Read all the lines, correct each **different** word once, using all CPU cores, then print the answers in the original order.

**Technically.**

1. The garbage collector is switched off (`debug.SetGCPercent(-1)`). The program runs for well under a second and uses under 200 MB, far below the 4 GB limit, so collecting garbage would only waste time.
2. Read all of stdin and split on `\n`. A trailing newline doesn't create an extra empty line. A read error exits with code 1. Lines are trimmed and lowercased.
3. **De-duplicate.** Each distinct word is corrected once.
4. **Worker pool.** `runtime.NumCPU()` goroutines each create their own `searcher` and take words from a channel. Every slot is written by exactly one worker, so no lock is needed.
5. **Write the output** through a buffered writer in input order, so the output always has as many lines as the input. If the final flush fails, it exits with code 1.

---

## 16. Data pipeline and tools (`tools/`)

| File | What it does |
|---|---|
| `fetch_data.sh` | Downloads SCOWL 2020.12.07, wordfreq 3.1.1's English frequency table and the Birkbeck misspelling corpora (the corpora are for evaluation only) |
| `build_lexicon.py` | Builds `data/lexicon.tsv` (details below) |
| `make_eval.py` | Builds `eval/tune.tsv` and `eval/holdout.tsv` (details below) |
| `score.py` | Runs `./autocorrect` on an eval file; prints accuracy per source and the time taken. `-v` lists the failures. |
| `sweep.sh` | Tries several values of one `AC_*` knob and scores each one, e.g. `tools/sweep.sh eval/tune.tsv PHONE "10 15 20"` |

**`build_lexicon.py`:**

- Keeps the lowest SCOWL level for each word, and only levels ≤ 70.
- Folds accents to ASCII (café → cafe).
- Flags names (`n`) and lowercase abbreviations (`a`).
- Takes each word's frequency from wordfreq and scales it to a whole count.

**`make_eval.py`:**

- Takes real misspelling pairs and splits them by **an MD5 hash of the correct word**.
- Mixes in common correct words, to measure over-correction.
- Adds synthetic one-key slips (drop, double, neighbour substitute or insert, swap).

---

## 17. Methodology: why the results can be trusted

**In simple words.** The program was tuned on one half of the test data and checked on the other half it had never seen. Both halves score about the same, so the settings weren't overfitted. The official development set was only used to report the final score.

**Technically.**

- **Each data source has one job.**
  - SCOWL decides *what is a word*.
  - wordfreq only decides *how common* a word is.
  - Birkbeck is never read by the program, and the program contains no misspelling→correction pairs.
- **Hash split by correct word.** Every misspelling of "receive" lands on the same side. A setting can't "learn" a word on tune and get credit for it on holdout.
- **Few knobs, no word-specific rules.** 13 numbers, chosen on tune and confirmed on holdout. Every rule is general (keyboard, sound, doubling, consonant clusters).
- **Results:**

  | Set | tune | holdout |
  |---|---:|---:|
  | Wikipedia misspellings | 83.9% | 86.1% |
  | Aspell misspellings | 64.1% | 65.7% |
  | Synthetic keyboard slips | 92.6% | 91.0% |
  | Correct words kept | 100% / 99.7% | 100% / 99.6% |
  | Children's errors (missp) | 46.1% | 43.4% |

  Official dev set (2,000 words): A = 0.9345, T = 0.12 s, 167 MB peak memory.
- **Tests:**
  - `main_test.go`: known cases such as `teh`→`the`, `fone`→`phone`, `dont`→`don't`, `seperate`→`separate`; identical answers for every `firstPass`; sound-key examples.
  - `search_test.go`: the pruned search against an exhaustive search with no pruning, on 431 dictionary words and 791 inputs, for several bonus and penalty values, with direct scoring on and off.
  - Output was also checked to be byte-identical on 1, 3 and 10 threads (24,111 words).

---

## 18. Known limits

- **One word at a time, no context.** `their` vs `there`, or `tast` = `taste` or `test`, can't be told apart without the surrounding sentence.
- **Garbage still gets corrected.** `xyzzyq` → `dizzy`, because the edit limit of 260 is generous.
- **Two-edit errors lose a little.** Preferring single-edit candidates helps most misspellings but costs about 1 point on errors two edits away.
- **Children's phonetic spellings are hard** (about 45%). They are often several edits away from the right word.
- **ASCII lowercase only**, by design of the input format.

---

## Glossary

| Term | Meaning |
|---|---|
| **Lexicon** | The dictionary of accepted words |
| **SCOWL level** | How obscure a word list is (10 = very common, 70 = rare but real, 80+ = includes misspellings) |
| **Prior** | Cost for how rare a word is; −log of its probability |
| **Edit distance** | Minimum total cost of edits that turns one word into another |
| **Damerau–Levenshtein** | Edit distance that also counts swapping two neighbouring letters as one edit |
| **Trie** | Tree of letters where words with the same start share a path |
| **DP row** | One row of the edit-distance grid (dynamic programming = building answers from smaller answers) |
| **Branch and bound** | Search that skips a branch once a lower bound shows it can't beat the best so far |
| **Exact pruning** | Pruning that never changes the final answer, only the speed |
| **Metaphone** | Algorithm that turns a word into a rough code for its sound |
| **Grapheme** | A letter or group of letters that spells one sound (`ph`, `tion`) |
| **Holdout set** | Test data never used for tuning, kept to check the settings generalise |
