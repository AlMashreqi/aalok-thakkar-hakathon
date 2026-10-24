package main

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// Independent, forward edge-relaxation oracle: no trie, pruning or row minima.
func referenceDistance(dict, typo string) int32 {
	m, n := len(dict), len(typo)
	dp := make([][]int32, m+1)
	for j := range dp {
		dp[j] = make([]int32, n+1)
		for i := range dp[j] {
			dp[j][i] = math.MaxInt32 / 4
		}
	}
	dp[0][0] = 0
	path := append([]byte{0}, []byte(dict)...)
	put := func(j, i int, cost int32) { dp[j][i] = min32(dp[j][i], cost) }
	for j := 0; j <= m; j++ {
		for i := 0; i <= n; i++ {
			v := dp[j][i]
			if j < m {
				put(j+1, i, v+omitCost(path, j+1))
			}
			if i < n {
				c, fat := insCost([]byte(typo), i+1)
				if j > 0 && near[dict[j-1]][typo[i]] {
					c = min32(c, fat)
				}
				put(j, i+1, v+c)
			}
			if i < n && j < m {
				c := sub[dict[j]][typo[i]]
				if (i == 0 || j == 0) && dict[j] != typo[i] {
					c += firstExtra
				}
				put(j+1, i+1, v+c)
			}
			if i+1 < n && j < m && dict[j] == typo[i+1] && near[dict[j]][typo[i]] {
				_, fat := insCost([]byte(typo), i+1)
				put(j+1, i+2, v+fat)
			}
			if i+1 < n && j+1 < m && dict[j] == typo[i+1] && dict[j+1] == typo[i] && dict[j] != dict[j+1] {
				put(j+2, i+2, v+transCost)
			}
			for _, r := range rules {
				if strings.HasPrefix(dict[j:], r.dict) && strings.HasPrefix(typo[i:], r.typo) {
					put(j+len(r.dict), i+len(r.typo), v+r.cost)
				}
			}
		}
	}
	return dp[m][n]
}

func referenceCorrect(input string) string {
	if lookup(input) || abbr[input] || len(input) >= maxDepth {
		return input
	}
	best, bestEdit, bestWord := int32(math.MaxInt32), int32(math.MaxInt32), -1
	key := phonKey(input)
	skey := soundKey(input)
	for i, w := range words {
		edit := referenceDistance(w, input)
		if edit > maxEdit {
			continue
		}
		total := edit + prior[i]
		if phon[i] == key {
			total -= phoneBonus
		}
		if soundKey(w) == skey {
			total -= soundBonus
		}
		if multiPen > 0 && !withinOne(w, input) {
			total += multiPen
		}
		if total < best || total == best && (edit < bestEdit || edit == bestEdit && i < bestWord) {
			best, bestEdit, bestWord = total, edit, i
		}
	}
	if bestWord < 0 {
		return input
	}
	return words[bestWord]
}

func TestPruningAgainstExhaustive(t *testing.T) {
	savedWords, savedPrior, savedPhon, savedSound, savedTrie, savedAbbr := words, prior, phon, snd, trie, abbr
	savedByPhon, savedBySnd, savedDirect := byPhon, bySnd, directMax
	savedFirst, savedMulti, savedBonus := firstPass, multiPen, soundBonus
	t.Cleanup(func() {
		words, prior, phon, snd, trie, abbr = savedWords, savedPrior, savedPhon, savedSound, savedTrie, savedAbbr
		byPhon, bySnd, directMax = savedByPhon, savedBySnd, savedDirect
		firstPass, multiPen, soundBonus = savedFirst, savedMulti, savedBonus
	})
	rng := rand.New(rand.NewSource(734))
	alphabet := "acdefghiknoprstuy'"
	makeWord := func() string {
		b := make([]byte, 2+rng.Intn(7))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	set := map[string]bool{"an": true, "na": true}
	inputs := []string{"amn", "anm", "nma", "mna"}
	for i := 0; i < 80; i++ {
		set[makeWord()] = true
	}
	// Exercise unfinished multi-letter rules across branch-and-bound frontiers.
	for _, r := range rules {
		set["a"+r.dict+"n"] = true
		inputs = append(inputs, "a"+r.typo+"n")
	}
	words, prior, phon, snd, abbr = nil, nil, nil, nil, map[string]bool{}
	trie = []node{{word: -1, first: -1, next: -1, minPrior: math.MaxInt32}}
	for w := range set {
		words = append(words, w)
	}
	sort.Strings(words)
	for i, w := range words {
		n := int32(0)
		for j := range w {
			n = child(n, w[j])
		}
		trie[n].word = int32(i)
		prior = append(prior, int32(rng.Intn(180)))
		phon = append(phon, phonKey(w))
		snd = append(snd, soundKey(w))
		if i%4 == 0 && len(w) > 2 {
			inputs = append(inputs, w[1:], w[:1]+w, w[1:2]+w[:1]+w[2:])
		}
	}
	for i := 0; i < 50; i++ {
		inputs = append(inputs, makeWord())
	}
	setMinPrior(0)
	buildKeyIndex()
	s := newSearcher()
	t.Logf("exhaustive oracle: %d dictionary entries, %d inputs, 3 sound bonuses, 4 penalties, 4 first-pass bounds", len(words), len(inputs))
	for _, bonus := range []int32{0, 50, 60} {
		soundBonus = bonus
		for _, penalty := range []int32{0, 25, 50, 60} {
			multiPen = penalty
			for _, input := range inputs {
				want := referenceCorrect(input)
				for _, fp := range []int32{0, 60, 140, maxEdit} {
					firstPass = fp
					for _, dm := range []int{0, 400} { // bonus words via the trie only, or scored directly
						directMax = dm
						if got := s.correct(input); got != want {
							t.Fatalf("sound=%d multi=%d firstPass=%d directMax=%d input=%q got=%q want=%q", bonus, penalty, fp, dm, input, got, want)
						}
					}
				}
			}
		}
	}
}

func TestWithinOne(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"abcd", "abcd", true}, {"abcd", "abxcd", true}, {"abcd", "acd", true},
		{"abcd", "abxd", true}, {"abcd", "acbd", true}, {"abcd", "xbcd", true},
		{"abcd", "axyd", false}, {"abcd", "ab", false}, {"abcd", "badc", false},
	} {
		if got := withinOne(c.a, c.b); got != c.want {
			t.Errorf("withinOne(%q,%q)=%t", c.a, c.b, got)
		}
		if got := withinOne(c.b, c.a); got != c.want {
			t.Errorf("reverse withinOne(%q,%q)=%t", c.b, c.a, got)
		}
	}
}
