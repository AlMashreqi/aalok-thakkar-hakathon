// autocorrect: reads one word per line on stdin, writes one corrected word per line.
//
// Method (no machine learning, fully deterministic):
//  1. A word found in the lexicon (SCOWL lists, incl. abbreviations) is returned unchanged.
//  2. Otherwise every lexicon word is scored with a hand-written weighted edit
//     distance (keyboard, phonetic and doubled-letter rules) plus a frequency
//     prior and a small penalty beyond one ordinary edit, minus bonuses when it
//     sounds like the input, via an exact branch-and-bound trie search.
//  3. The cheapest candidate wins; if none is within the bound the word is kept.
package main

import (
	"bufio"
	_ "embed"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/lexicon.tsv
var lexiconTSV string

// Tunable constants (costs are in hundredths of one plain edit).
// Each can be overridden by an AC_<NAME> environment variable for tuning runs.
var (
	maxLevel    = envInt("MAXLEVEL", 70)          // SCOWL size level accepted as a real word
	priorScale  = envInt("PRIORSCALE", 75)        // cost per 10 natural-log units of frequency ratio
	zeroCount   = envInt("ZEROCOUNT", 500)        // fallback in the embedded wordfreq count units
	maxEdit     = int32(envInt("MAXEDIT", 260))   // search bound on edit cost
	firstExtra  = int32(envInt("FIRST", 50))      // extra cost for an edit touching the first letter
	firstPass   = int32(envInt("FIRSTPASS", 140)) // edit bound of the quick first search pass
	phoneBonus  = int32(envInt("PHONE", 15))      // discount for a candidate that sounds like the input
	soundBonus  = int32(envInt("SOUND", 50))      // extra discount when vowel sounds match too (soundKey)
	clusterCost = int32(envInt("CLUSTER", 40))    // dropping a weak consonant (n/m/r/l) from a cluster; 0 = off
	aposCost    = int32(envInt("APOS", 60))       // cost of a missing or extra apostrophe
	namePen     = int32(envInt("NAMEPEN", 80))    // extra prior for words SCOWL lists only as names
	levelPen    = int32(envInt("LEVELPEN", 120))  // extra prior per 100 SCOWL levels (rarer word lists)
	multiPen    = int32(envInt("MULTI", 60))      // extra cost for candidates beyond one ordinary edit
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv("AC_" + name)); err == nil {
		return v
	}
	return def
}

type node struct {
	ch          byte
	word        int32 // index into words, -1 if none
	minPrior    int32 // smallest prior of any word in this subtree
	first, next int32 // first child, next sibling (-1 terminated)
}

var (
	words []string
	prior []int32
	phon  []string // phonetic key of each word
	snd   []string // vowel-aware sound key of each word
	// words by phonetic / sound key, so bonus candidates can be scored directly
	byPhon, bySnd map[string][]int32
	trie          []node
	abbr          = map[string]bool{} // kept when typed, never offered as a correction
)

func load() {
	maxCount := 0.0
	type entry struct {
		w    string
		c    float64
		lv   int32
		name bool
	}
	var es []entry
	for line := range strings.SplitSeq(lexiconTSV, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		lv, _ := strconv.Atoi(f[1])
		if lv > maxLevel {
			continue
		}
		if f[3] == "a" {
			abbr[f[0]] = true
			continue
		}
		c, _ := strconv.ParseFloat(f[2], 64)
		if c == 0 {
			c = float64(zeroCount)
		}
		maxCount = math.Max(maxCount, c)
		es = append(es, entry{f[0], c, int32(lv), f[3] == "n"})
	}
	trie = append(trie, node{word: -1, first: -1, next: -1, minPrior: math.MaxInt32})
	for _, e := range es {
		n := int32(0)
		for k := 0; k < len(e.w); k++ {
			n = child(n, e.w[k])
		}
		trie[n].word = int32(len(words))
		words = append(words, e.w)
		p := int32(math.Round(float64(priorScale) / 10 * (math.Log(maxCount) - math.Log(e.c))))
		p += levelPen * e.lv / 100
		if e.name {
			p += namePen
		}
		prior = append(prior, p)
	}
	// phonetic keys are independent per word: compute them on all cores
	phon, snd = make([]string, len(words)), make([]string, len(words))
	var wg sync.WaitGroup
	chunk := (len(words) + runtime.NumCPU() - 1) / runtime.NumCPU()
	for lo := 0; lo < len(words); lo += chunk {
		hi := min(lo+chunk, len(words))
		wg.Go(func() {
			for i := lo; i < hi; i++ {
				phon[i], snd[i] = phonKey(words[i]), soundKey(words[i])
			}
		})
	}
	setMinPrior(0)
	wg.Wait()
	buildKeyIndex()
}

// buildKeyIndex fills byPhon and bySnd from phon and snd.
func buildKeyIndex() {
	byPhon, bySnd = map[string][]int32{}, map[string][]int32{}
	var wg sync.WaitGroup
	for _, x := range []struct {
		m    map[string][]int32
		keys []string
	}{{byPhon, phon}, {bySnd, snd}} {
		wg.Go(func() {
			for i, k := range x.keys {
				x.m[k] = append(x.m[k], int32(i))
			}
		})
	}
	wg.Wait()
}

func setMinPrior(n int32) int32 {
	m := int32(math.MaxInt32)
	if w := trie[n].word; w >= 0 {
		m = prior[w]
	}
	for c := trie[n].first; c >= 0; c = trie[c].next {
		m = min32(m, setMinPrior(c))
	}
	trie[n].minPrior = m
	if first := trie[n].first; first < 0 || trie[first].next < 0 {
		return m // no allocation or sorting for a leaf or a single child
	}
	// visit frequent subtrees first so a good candidate tightens the bound early
	var kids []int32
	for c := trie[n].first; c >= 0; c = trie[c].next {
		kids = append(kids, c)
	}
	sort.SliceStable(kids, func(a, b int) bool { return trie[kids[a]].minPrior < trie[kids[b]].minPrior })
	trie[n].first = -1
	for k := len(kids) - 1; k >= 0; k-- {
		trie[kids[k]].next, trie[n].first = trie[n].first, kids[k]
	}
	return m
}

// child returns (creating if needed) the child of n labelled ch; siblings stay sorted.
func child(n int32, ch byte) int32 {
	prev, c := int32(-1), trie[n].first
	for c >= 0 && trie[c].ch < ch {
		prev, c = c, trie[c].next
	}
	if c >= 0 && trie[c].ch == ch {
		return c
	}
	trie = append(trie, node{ch: ch, word: -1, first: -1, next: c, minPrior: math.MaxInt32})
	id := int32(len(trie) - 1)
	if prev < 0 {
		trie[n].first = id
	} else {
		trie[prev].next = id
	}
	return id
}

func lookup(w string) bool {
	n := int32(0)
	for k := 0; k < len(w) && n >= 0; k++ {
		c := trie[n].first
		for c >= 0 && trie[c].ch != w[k] {
			c = trie[c].next
		}
		n = c
	}
	return n >= 0 && trie[n].word >= 0
}

// ---- error model -----------------------------------------------------------

var vowel = func() (v [256]bool) {
	for _, c := range "aeiouy" {
		v[c] = true
	}
	return v
}()

func isVowel(c byte) bool { return vowel[c] }

var kbRows = []string{"qwertyuiop", "asdfghjkl", "zxcvbnm"}

// sub[a][b]: cost of the intended letter a being typed as b.
var sub [256][256]int32
var near [256][256]bool // keyboard geometry is independent of phonetic substitution costs

// rule: the dictionary spelling dict was written as typo (sound-alike graphemes).
type rule struct {
	typo, dict string
	cost       int32
	dk, mask   uint64 // dict side packed 5 bits per letter (last letter lowest), and its mask
}

var (
	rules       []rule
	maxRuleDict int     // longest dictionary side of any rule
	rulePrefix  []int32 // packed proper prefix of a rule's dict side -> cheapest such rule (0 = none)
)

func init() {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			sub[a][b] = 100
		}
	}
	// keyboard neighbours (QWERTY, rows offset by half a key)
	for r, row := range kbRows {
		for i := 0; i < len(row); i++ {
			for r2, row2 := range kbRows {
				for i2 := 0; i2 < len(row2); i2++ {
					dx := float64(i2) + 0.5*float64(r2) - float64(i) - 0.5*float64(r)
					if (r != r2 || i != i2) && abs(r-r2) <= 1 && math.Abs(dx) <= 1 {
						sub[row[i]][row2[i2]] = 75
						near[row[i]][row2[i2]] = true
					}
				}
			}
		}
	}
	const vowels = "aeiouy"
	for x := 0; x < len(vowels); x++ {
		for y := 0; y < len(vowels); y++ {
			sub[vowels[x]][vowels[y]] = 60
		}
	}
	// letters that stand for the same or a similar sound
	for _, p := range []string{"ck", "cs", "sz", "kq", "gj", "fv", "dt", "bp", "mn", "sc", "xz", "xs", "iy", "ey", "wu"} {
		sub[p[0]][p[1]], sub[p[1]][p[0]] = 60, 60
	}
	for a := 0; a < 256; a++ {
		sub[a][a] = 0
	}
	// sound-alike spellings, usable in both directions
	pairs := []struct {
		a, b string
		c    int32
	}{
		{"ph", "f", 40}, {"gh", "f", 60}, {"ck", "k", 30}, {"ck", "c", 30}, {"ch", "k", 50}, {"ch", "c", 50},
		{"qu", "kw", 40}, {"cqu", "kw", 40}, {"qu", "q", 60}, {"ch", "sh", 60}, {"tch", "ch", 30},
		{"x", "ks", 40}, {"x", "cks", 40}, {"x", "cs", 40}, {"sc", "s", 40}, {"sc", "sk", 40}, {"ps", "s", 50},
		{"kn", "n", 40}, {"wr", "r", 40}, {"wh", "w", 40}, {"mb", "m", 50}, {"gn", "n", 50}, {"gu", "g", 50},
		{"dg", "j", 50}, {"dge", "j", 50}, {"ge", "j", 60}, {"tion", "sion", 40}, {"tion", "shun", 40},
		{"sion", "shun", 40}, {"cian", "shun", 40}, {"tial", "shal", 50}, {"cial", "shal", 50},
		{"cious", "shus", 50}, {"ture", "cher", 50}, {"eau", "o", 50}, {"eau", "u", 50}, {"au", "o", 50},
		{"aw", "o", 60}, {"ou", "u", 50}, {"ou", "o", 50}, {"ow", "o", 50}, {"oo", "u", 40}, {"ew", "u", 50},
		{"ue", "u", 50}, {"ee", "ea", 40}, {"ee", "ie", 40}, {"ee", "ei", 40}, {"ea", "e", 50}, {"ai", "a", 50},
		{"ay", "a", 50}, {"ey", "y", 50}, {"le", "el", 40}, {"re", "er", 40}, {"our", "or", 40},
		{"ay", "ai", 40}, {"oa", "o", 50}, {"oe", "o", 50}, {"ui", "u", 50}, {"ue", "oo", 50}, {"ew", "oo", 50},
		{"eigh", "ay", 50}, {"eigh", "a", 60}, {"igh", "y", 50}, {"bt", "t", 50}, {"ea", "ee", 40}, {"ie", "y", 50},
		{"ie", "ei", 35}, {"ys", "ies", 40}, {"ure", "er", 50}, {"ough", "u", 50}, {"ough", "o", 60}, {"ough", "off", 60}, {"ough", "uff", 60}, {"augh", "aff", 60}, {"igh", "i", 60},
	}
	// weak consonants left out of clusters, a well-known pattern in human spelling
	// (Treiman 1993): nasals before a consonant (angina -> agina), r/l after a vowel
	// before a consonant (field -> fied), r/l after a consonant (scrub -> scub).
	// One direction only: the dictionary has the cluster, the typo lacks the letter.
	if clusterCost > 0 {
		const cons = "bcdfghjklmnpqrstvwxz"
		for _, c := range cons {
			for _, w := range "nm" {
				if c != w {
					rules = append(rules, rule{string(c), string(w) + string(c), clusterCost, 0, 0})
				}
			}
			for _, w := range "rl" {
				if c != w {
					rules = append(rules, rule{string(c), string(c) + string(w), clusterCost, 0, 0})
					for _, v := range "aeiou" {
						rules = append(rules, rule{string(v) + string(c), string(v) + string(w) + string(c), clusterCost, 0, 0})
					}
				}
			}
		}
	}
	for _, p := range pairs {
		rules = append(rules, rule{p.a, p.b, p.c, 0, 0}, rule{p.b, p.a, p.c, 0, 0})
		maxRuleDict = max(maxRuleDict, len(p.a), len(p.b))
	}
	for i := range rules {
		r := &rules[i]
		for k := 0; k < len(r.dict); k++ {
			r.dk = r.dk<<5 | uint64(code[r.dict[k]])
		}
		r.mask = 1<<(5*len(r.dict)) - 1
	}
	rulePrefix = make([]int32, 1<<(5*(maxRuleDict-1)))
	for _, r := range rules {
		for l := 1; l < len(r.dict); l++ {
			k := pack(r.dict[:l])
			if c := rulePrefix[k]; c == 0 || r.cost < c {
				rulePrefix[k] = r.cost
			}
		}
	}
}

// pack encodes a short string 5 bits per letter (a-z, apostrophe), first letter highest.
func pack(s string) int {
	k := 0
	for i := 0; i < len(s); i++ {
		k = k<<5 | int(code[s[i]])
	}
	return k
}

// code: a-z -> 1..26, apostrophe -> 27, anything else 0 (package-level so it is
// ready before the init that packs rule prefixes).
var code = func() (c [256]byte) {
	for ch := 'a'; ch <= 'z'; ch++ {
		c[ch] = byte(ch - 'a' + 1)
	}
	c['\''] = 27
	return c
}()

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// omit: cost of the intended letter d (at dictionary position j) being left out.
func omitCost(path []byte, j int) int32 {
	d := path[j]
	var c int32 = 100
	switch {
	case path[j-1] == d: // doubled letter written once
		c = 40
	case d == '\'':
		c = aposCost
	case isVowel(d) || d == 'h':
		c = 70
	}
	if j == 1 {
		c += firstExtra
	}
	return c
}

// insert: cost of an extra typed letter t[i-1] (1-based i); a stray key pressed
// beside the intended one (fat finger) costs insFat instead when that is cheaper.
func insCost(t []byte, i int) (c, insFat int32) {
	x := t[i-1]
	c, insFat = 100, 80
	switch {
	case i >= 2 && t[i-2] == x: // letter doubled by mistake
		c = 40
	case x == '\'':
		c = aposCost
	case isVowel(x) || x == 'h':
		c = 70
	}
	if i == 1 {
		c += firstExtra
		insFat += firstExtra
	}
	return c, insFat
}

// phonKey is a simplified Metaphone: spellings of the same sound map to one
// letter, vowels after the first letter and silent letters are dropped, and
// repeats collapse. Two words with equal keys usually sound alike.
func phonKey(w string) string {
	w = strings.ReplaceAll(w, "'", "")
	at := func(i int) byte {
		if i >= 0 && i < len(w) {
			return w[i]
		}
		return 0
	}
	soft := func(c byte) bool { return c == 'e' || c == 'i' || c == 'y' }
	var k []byte
	for i := 0; i < len(w); i++ {
		c, nx := w[i], at(i+1)
		var out byte
		switch {
		case i == 0 && strings.Contains("kn gn ps wr", string([]byte{c, nx})):
			continue // silent first letter
		case c == 't' && nx == 'c' && at(i+2) == 'h':
			continue // tch sounds as ch
		case isVowel(c):
			if i == 0 {
				out = 'a'
			}
		case c == 'c':
			switch {
			case nx == 'h':
				out, i = 'x', i+1
			case soft(nx):
				out = 's'
			default:
				out = 'k'
			}
		case c == 's' && nx == 'h', c == 't' && nx == 'i' && (at(i+2) == 'o' || at(i+2) == 'a'):
			out = 'x'
		case c == 'p' && nx == 'h':
			out, i = 'f', i+1
		case c == 't' && nx == 'h':
			out, i = '0', i+1
		case c == 'g' && nx == 'h':
			if i == 0 {
				out = 'k'
			}
			i++
		case c == 'g' && soft(nx), c == 'j':
			out = 'j'
		case c == 'd' && nx == 'g':
			continue // dg sounds as the following g
		case c == 'q', c == 'k':
			out = 'k'
		case c == 'x':
			if i == 0 {
				out = 's'
			} else {
				k = append(k, 'k')
				out = 's'
			}
		case c == 'z':
			out = 's'
		case c == 'v':
			out = 'f'
		case c == 'h', c == 'w':
			if i == 0 && isVowel(nx) {
				out = c
			}
		case c == 'b' && i == len(w)-1 && at(i-1) == 'm':
			continue // silent final b: lamb
		default:
			out = c
		}
		if out != 0 && (len(k) == 0 || k[len(k)-1] != out) {
			k = append(k, out)
		}
	}
	return string(k)
}

// soundKey keeps vowel sounds, unlike phonKey: hand-written grapheme rules map
// spellings of one sound (ee/ea, oa, o-consonant-e, ...) to one symbol, so
// "wead" matches "weed" but not "wed". Lowercase = short vowel, uppercase = long.
var soundRules = []struct {
	g, s string
	end  bool // rule applies only at the end of the word
}{
	{"tion", "SHN", false}, {"sion", "SHN", false}, {"cian", "SHN", false}, {"ough", "O", false},
	{"augh", "O", false}, {"eigh", "A", false}, {"igh", "Y", false}, {"tch", "C", false},
	{"sch", "SK", false}, {"ph", "F", false}, {"gh", "", false}, {"ck", "K", false},
	{"qu", "KW", false}, {"wh", "W", false}, {"wr", "R", false}, {"kn", "N", false},
	{"gn", "N", false}, {"mb", "M", true}, {"ee", "I", false}, {"ea", "I", false},
	{"ie", "Y", true}, {"ie", "I", false}, {"ei", "I", false}, {"ey", "I", true},
	{"oa", "O", false}, {"oe", "O", false}, {"ow", "O", true}, {"ou", "W", false},
	{"ow", "W", false}, {"oo", "U", false}, {"ew", "U", false}, {"ue", "U", false},
	{"ui", "U", false}, {"ai", "A", false}, {"ay", "A", false}, {"au", "O", false},
	{"aw", "O", false}, {"oi", "E", false}, {"oy", "E", false}, {"ar", "AR", false},
	{"er", "R", false}, {"ir", "R", false}, {"ur", "R", false}, {"or", "OR", false},
	{"ch", "C", false}, {"sh", "X", false}, {"th", "0", false},
}

// consonant sounds for soundKey (arrays, not maps: soundKey runs for every word at startup)
var consSound, softSound [256]string

func init() {
	for c, s := range map[byte]string{'c': "K", 'k': "K", 'q': "K", 'x': "KS", 'z': "S", 's': "S", 'j': "J",
		'g': "G", 'd': "D", 't': "T", 'b': "B", 'p': "P", 'f': "F", 'v': "V", 'w': "W",
		'h': "H", 'l': "L", 'm': "M", 'n': "N", 'r': "R"} {
		consSound[c] = s
	}
	softSound['c'], softSound['g'] = "S", "J"
}

func soundKey(w string) string {
	w = strings.ReplaceAll(w, "'", "")
	n := len(w)
	var k []byte
	put := func(s string) {
		for i := 0; i < len(s); i++ {
			if len(k) == 0 || k[len(k)-1] != s[i] {
				k = append(k, s[i])
			}
		}
	}
outer:
	for i := 0; i < n; {
		for _, r := range soundRules {
			if r.g[0] == w[i] && strings.HasPrefix(w[i:], r.g) && (!r.end || i+len(r.g) == n) {
				put(r.s)
				i += len(r.g)
				continue outer
			}
		}
		c := w[i]
		switch {
		case isVowel(c) || c == 'y':
			switch {
			case i+2 == n-1 && w[n-1] == 'e' && !isVowel(w[i+1]): // magic e: vowel, consonant, final e
				put(string("AIYOUY"[strings.IndexByte("aeiouy", c)]))
			case c == 'e' && i == n-1 && n > 2: // silent final e
			case c == 'y' && i == 0:
				put("j")
			case c == 'y':
				put("i")
			default:
				put(string(c))
			}
		case (c == 'c' || c == 'g') && i+1 < n && strings.IndexByte("eiy", w[i+1]) >= 0:
			put(softSound[c])
		default:
			put(consSound[c])
		}
		i++
	}
	return string(k)
}

// ---- search ----------------------------------------------------------------

var directMax = 400 // most bonus candidates scored directly before the trie search

const (
	maxDepth  = 64
	transCost = 70 // two adjacent letters swapped
)

type searcher struct {
	t        []byte
	rows     [maxDepth + 1][]int32
	rowMin   [maxDepth + 1]int32
	ins, fat [maxDepth + 1]int32 // insCost per typo position
	path     [maxDepth + 1]byte
	pk       [maxDepth + 1]uint64 // pk[j]: path[1..j] packed like rule.dk (letters beyond 12 shift out)
	rmatch   [][28][]int32        // rmatch[i][c]: rules whose typo side ends at typo position i and dict side ends in letter code c
	bound    int32
	best     int32 // total score of bestWord
	bestEdit int32
	bestWord int32
	key      string // phonetic key of the input
	skey     string // sound key of the input
	allow    int32  // bonus a not-yet-scored word may still get (0 once bonus words are scored directly)
}

func newSearcher() *searcher {
	s := &searcher{}
	for k := range s.rows {
		s.rows[k] = make([]int32, maxDepth+1)
	}
	return s
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func (s *searcher) correct(w string) string {
	if lookup(w) || abbr[w] || len(w) >= maxDepth {
		return w
	}
	t := []byte(w)
	n := len(t)
	s.t = t
	s.key = phonKey(w)
	s.skey = soundKey(w)
	s.rmatch = s.rmatch[:0]
	for i := 0; i <= n; i++ {
		var m [28][]int32
		for r, ru := range rules {
			if l := len(ru.typo); l <= i && string(t[i-l:i]) == ru.typo {
				c := code[ru.dict[len(ru.dict)-1]]
				m[c] = append(m[c], int32(r))
			}
		}
		s.rmatch = append(s.rmatch, m)
	}
	row := s.rows[0]
	row[0] = 0
	s.rowMin[0] = 0
	s.path[0] = 0
	for i := 1; i <= n; i++ {
		s.ins[i], s.fat[i] = insCost(t, i)
		row[i] = row[i-1] + s.ins[i]
	}
	s.best, s.bestEdit, s.bestWord = math.MaxInt32, math.MaxInt32, -1
	// Only words whose keys equal the input's can get a bonus. When there are few,
	// score them directly first; the trie search then needs no bonus allowance,
	// which makes its pruning much tighter.
	s.allow = phoneBonus + soundBonus
	if ph, sd := byPhon[s.key], bySnd[s.skey]; len(ph)+len(sd) <= directMax {
		s.bound = maxEdit
		for _, w := range ph {
			s.scoreWord(w)
		}
		for _, w := range sd {
			s.scoreWord(w)
		}
		s.allow = 0
	}
	// a cheap tight pass first finds a good incumbent, so the full pass prunes harder
	for _, b := range []int32{min32(firstPass, maxEdit), maxEdit} {
		s.bound = b
		if s.bestWord >= 0 {
			s.bound = min32(b, s.best+s.allow) // a discounted word may cost up to the allowance more to edit
		}
		for c := trie[0].first; c >= 0; c = trie[c].next {
			s.dfs(c, 1)
		}
	}
	if s.bestWord < 0 {
		return w
	}
	return words[s.bestWord]
}

// step computes DP row j for trie node nd (its letter is path[j]) and returns the row minimum.
func (s *searcher) step(nd int32, j int) int32 {
	t, n := s.t, len(s.t)
	s.path[j] = trie[nd].ch
	prev, row := s.rows[j-1], s.rows[j]
	d := s.path[j]
	s.pk[j] = s.pk[j-1]<<5 | uint64(code[d])
	pk := s.pk[j]
	omit := omitCost(s.path[:], j)
	row[0] = prev[0] + omit
	lo := row[0]
	for i := 1; i <= n; i++ {
		x := t[i-1]
		v := prev[i-1] + sub[d][x]
		if (i == 1 || j == 1) && d != x {
			v += firstExtra
		}
		v = min32(v, prev[i]+omit)
		ins := s.ins[i]
		if near[d][x] {
			ins = min32(ins, s.fat[i])
		}
		v = min32(v, row[i-1]+ins)
		if i >= 2 && x == d && near[d][t[i-2]] {
			v = min32(v, prev[i-2]+s.fat[i-1]) // stray key immediately before its intended neighbor
		}
		if i >= 2 && j >= 2 && s.path[j] == t[i-2] && s.path[j-1] == x && x != s.path[j] {
			v = min32(v, s.rows[j-2][i-2]+transCost)
		}
		for _, r := range s.rmatch[i][code[d]] {
			ru := &rules[r]
			if l := len(ru.dict); l <= j && pk&ru.mask == ru.dk {
				v = min32(v, s.rows[j-l][i-len(ru.typo)]+ru.cost)
			}
		}
		row[i] = v
		lo = min32(lo, v)
	}
	s.rowMin[j] = lo
	return lo
}

// consider scores word w at edit cost e; ties go to the smaller edit, then the
// earlier word, so the result does not depend on search order.
func (s *searcher) consider(w, e int32) {
	tot := e + prior[w]
	if multiPen > 0 && !withinOne(words[w], string(s.t)) {
		tot += multiPen // nonnegative: existing branch-and-bound lower bounds remain valid
	}
	if phon[w] == s.key {
		tot -= phoneBonus
	}
	if snd[w] == s.skey {
		tot -= soundBonus
	}
	if tot < s.best || tot == s.best && (e < s.bestEdit || e == s.bestEdit && w < s.bestWord) {
		s.best, s.bestEdit, s.bestWord = tot, e, w
		s.bound = min32(s.bound, tot+s.allow) // priors >= 0: no word with a larger edit can win
	}
}

// scoreWord scores one dictionary word directly by walking its own trie path.
func (s *searcher) scoreWord(w int32) {
	nd := int32(0)
	word := words[w]
	for k := 0; k < len(word); k++ {
		c := trie[nd].first
		for trie[c].ch != word[k] {
			c = trie[c].next
		}
		nd = c
		s.step(nd, k+1)
	}
	if e := s.rows[len(word)][len(s.t)]; e <= maxEdit {
		s.consider(w, e)
	}
}

func (s *searcher) dfs(nd int32, j int) {
	if j > maxDepth-1 {
		return
	}
	lo := s.step(nd, j)
	if w, e := trie[nd].word, s.rows[j][len(s.t)]; w >= 0 && e <= s.bound {
		s.consider(w, e)
	}
	// a rule whose dict side is under way can still jump from an earlier row;
	// any word below costs at least lo plus the subtree's best prior
	if j >= 1 {
		lo = min32(lo, s.rowMin[j-1]+transCost) // a swap reaches row j+1 from row j-1
	}
	key := 0
	for k := 1; k < maxRuleDict && k <= j; k++ {
		key |= int(code[s.path[j-k+1]]) << (5 * (k - 1))
		if c := rulePrefix[key]; c != 0 {
			lo = min32(lo, s.rowMin[j-k]+c)
		}
	}
	if lo > s.bound || lo+trie[nd].minPrior-s.allow > s.best {
		return
	}
	for c := trie[nd].first; c >= 0; c = trie[c].next {
		s.dfs(c, j+1)
	}
}

// withinOne recognizes one insertion, deletion, substitution, or adjacent swap.
func withinOne(a, b string) bool {
	if abs(len(a)-len(b)) > 1 {
		return false
	}
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	if i == len(a) || i == len(b) {
		return true
	}
	switch {
	case len(a) > len(b):
		return a[i+1:] == b[i:]
	case len(b) > len(a):
		return a[i:] == b[i+1:]
	default:
		return a[i+1:] == b[i+1:] || i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
	}
}

func main() {
	debug.SetGCPercent(-1) // short-lived process: memory stays far below the 4 GB limit, GC is pure overhead
	load()
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1) // incomplete input cannot produce a valid run
	}
	in := strings.Split(string(raw), "\n")
	if len(in) > 0 && in[len(in)-1] == "" {
		in = in[:len(in)-1] // trailing newline ends the last line, it does not start a new one
	}
	for k, w := range in {
		in[k] = strings.ToLower(strings.TrimSpace(w))
	}
	// correct each distinct word once, in parallel; output order is input order
	uniq := map[string]string{}
	var todo []string
	for _, w := range in {
		if _, ok := uniq[w]; !ok {
			uniq[w] = ""
			todo = append(todo, w)
		}
	}
	out := make([]string, len(todo))
	var wg sync.WaitGroup
	next := make(chan int, len(todo))
	for k := range todo {
		next <- k
	}
	close(next)
	for range runtime.NumCPU() {
		wg.Go(func() {
			s := newSearcher()
			for k := range next {
				out[k] = s.correct(todo[k])
			}
		})
	}
	wg.Wait()
	for k, w := range todo {
		uniq[w] = out[k]
	}
	bw := bufio.NewWriter(os.Stdout)
	for _, w := range in {
		if c := uniq[w]; c != "" {
			bw.WriteString(c)
		} else {
			bw.WriteString("a") // empty input line: any non-empty word keeps the line count valid
		}
		bw.WriteByte('\n')
	}
	if bw.Flush() != nil {
		os.Exit(1) // output is incomplete; do not report success
	}
}
