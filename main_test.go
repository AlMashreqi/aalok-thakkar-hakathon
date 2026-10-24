package main

import (
	"strings"
	"testing"
)

func TestCorrect(t *testing.T) {
	load()
	s := newSearcher()
	for typo, want := range map[string]string{
		"the": "the", "recieve": "receive", "teh": "the", "dont": "don't", "acommodate": "accommodate",
		"seperate": "separate", "definately": "definitely", "fone": "phone", "occured": "occurred",
	} {
		if got := s.correct(typo); got != want {
			t.Errorf("correct(%q) = %q, want %q", typo, got, want)
		}
	}
	// pruning must be exact: the answer may not depend on the first-pass bound
	words := strings.Fields("rbidge tarpasn ienna hte wierd beleive goverment untill wich thier becuase")
	var ref []string
	for _, w := range words {
		ref = append(ref, s.correct(w))
	}
	defer func(fp int32) { firstPass = fp }(firstPass)
	for _, fp := range []int32{0, 60, 260} {
		firstPass = fp
		for k, w := range words {
			if got := s.correct(w); got != ref[k] {
				t.Errorf("firstPass=%d: correct(%q) = %q, want %q", fp, w, got, ref[k])
			}
		}
	}
}
