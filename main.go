// autocorrect: reads one word per line on stdin, writes one corrected word per line.
//
// Method (no machine learning, fully deterministic):
//  1. A word found in the lexicon (SCOWL lists) is returned unchanged.
//  2. Otherwise every lexicon word is scored with a hand-written weighted edit
//     distance (keyboard, phonetic and doubled-letter rules) plus a frequency
//     prior, via a depth-first walk over a trie with branch-and-bound pruning.
//  3. The cheapest candidate wins; if none is within the bound the word is kept.
package main

import (
	"bufio"
	_ "embed"
	"io"
	"math"
	"os"
	"runtime"
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
	maxLevel   = envInt("MAXLEVEL", 70)          // SCOWL size level accepted as a real word
	priorScale = envInt("PRIORSCALE", 90)        // cost per 10 natural-log units of frequency ratio
	zeroCount  = envInt("ZEROCOUNT", 5000)       // assumed count for lexicon words absent from count_1w
	maxEdit    = int32(envInt("MAXEDIT", 260))   // search bound on edit cost
	firstExtra = int32(envInt("FIRST", 50))      // extra cost for an edit touching the first letter
	firstPass  = int32(envInt("FIRSTPASS", 140)) // edit bound of the quick first search pass
	phoneBonus = int32(envInt("PHONE", 30))      // discount for a candidate that sounds like the input
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
	trie  []node
)

func load() {
	maxCount := 0.0
	type entry struct {
		w string
		c float64
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
		c, _ := strconv.ParseFloat(f[2], 64)
		if c == 0 {
			c = float64(zeroCount)
		}
		maxCount = math.Max(maxCount, c)
		es = append(es, entry{f[0], c})
	}
	trie = append(trie, node{word: -1, first: -1, next: -1, minPrior: math.MaxInt32})
	for _, e := range es {
		n := int32(0)
		for k := 0; k < len(e.w); k++ {
			n = child(n, e.w[k])
		}
		trie[n].word = int32(len(words))
		words = append(words, e.w)
		prior = append(prior, int32(math.Round(float64(priorScale)/10*(math.Log(maxCount)-math.Log(e.c)))))
		phon = append(phon, phonKey(e.w))
	}
	setMinPrior(0)
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

// rule: the dictionary spelling dict was written as typo (sound-alike graphemes).
type rule struct {
	typo, dict string
	cost       int32
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
	for _, p := range pairs {
		rules = append(rules, rule{p.a, p.b, p.c}, rule{p.b, p.a, p.c})
		maxRuleDict = max(maxRuleDict, len(p.a), len(p.b))
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
		c = 30
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
		c = 30
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

// ---- search ----------------------------------------------------------------

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
	rmatch   [][]int32 // rmatch[i]: indices of rules whose typo side ends at typo position i
	bound    int32
	best     int32 // total (edit + prior) of bestWord
	bestEdit int32
	bestWord int32
	key      string // phonetic key of the input
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
	if lookup(w) || len(w) >= maxDepth {
		return w
	}
	t := []byte(w)
	n := len(t)
	s.t = t
	s.key = phonKey(w)
	s.rmatch = s.rmatch[:0]
	for i := 0; i <= n; i++ {
		var m []int32
		for r, ru := range rules {
			if l := len(ru.typo); l <= i && string(t[i-l:i]) == ru.typo {
				m = append(m, int32(r))
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
	// a cheap tight pass first finds a good incumbent, so the full pass prunes harder
	s.best, s.bestEdit, s.bestWord = math.MaxInt32, math.MaxInt32, -1
	for _, b := range []int32{min32(firstPass, maxEdit), maxEdit} {
		s.bound = b
		if s.bestWord >= 0 {
			s.bound = min32(b, s.best+phoneBonus) // a discounted word may cost up to phoneBonus more to edit
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

func (s *searcher) dfs(nd int32, j int) {
	if j > maxDepth-1 {
		return
	}
	t, n := s.t, len(s.t)
	s.path[j] = trie[nd].ch
	prev, row := s.rows[j-1], s.rows[j]
	d := s.path[j]
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
		if sub[d][x] == 75 {
			ins = min32(ins, s.fat[i])
		}
		v = min32(v, row[i-1]+ins)
		if i >= 2 && j >= 2 && s.path[j] == t[i-2] && s.path[j-1] == x && x != s.path[j] {
			v = min32(v, s.rows[j-2][i-2]+transCost)
		}
		for _, r := range s.rmatch[i] {
			ru := &rules[r]
			if l := len(ru.dict); l <= j && string(s.path[j-l+1:j+1]) == ru.dict {
				v = min32(v, s.rows[j-l][i-len(ru.typo)]+ru.cost)
			}
		}
		row[i] = v
		lo = min32(lo, v)
	}
	if w := trie[nd].word; w >= 0 && row[n] <= s.bound {
		// ties go to the smaller edit, then the earlier word: independent of search order
		tot := row[n] + prior[w]
		if phon[w] == s.key {
			tot -= phoneBonus
		}
		if tot < s.best || tot == s.best && (row[n] < s.bestEdit || row[n] == s.bestEdit && w < s.bestWord) {
			s.best, s.bestEdit, s.bestWord = tot, row[n], w
			s.bound = min32(s.bound, tot+phoneBonus) // priors >= 0, bonus <= phoneBonus: nothing costlier can win
		}
	}
	s.rowMin[j] = lo
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
	if lo > s.bound || lo+trie[nd].minPrior-phoneBonus > s.best {
		return
	}
	for c := trie[nd].first; c >= 0; c = trie[c].next {
		s.dfs(c, j+1)
	}
}

func main() {
	load()
	raw, _ := io.ReadAll(os.Stdin)
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
