package main

import (
	"regexp"
	"strings"
)

// A deliberately small filter for slurs in public text (chat and the ticker).
// Everything else, swearing included, is left alone. It sees through common
// disguises: l33t spelling, spaced-out letters and stretched letters.

// leet maps lookalike characters to the letters they stand in for.
var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a", "$", "s", "!", "i", "|", "i")

var (
	nonLetters = regexp.MustCompile(`[^a-z]+`)

	// Checked against the whole message with everything but letters removed, so
	// "n i g g e r" and "n.i.g.g.a" are caught. The leading [^s] spares "snigger".
	blockedRuns = []*regexp.Regexp{
		regexp.MustCompile(`(^|[^s])n+i+g{2,}(a|ah|as|az|er|ers|erz|uh|let|lets)`),
		regexp.MustCompile(`f+a+g{2,}o+t`),
	}

	// Checked against single words with repeated letters squashed, for slurs
	// that are too short or common-looking to search for inside other words.
	blockedWords = map[string]bool{
		"niga": true, "nigas": true, "nigaz": true, "nigah": true,
		"kike": true, "kikes": true,
		"spic": true, "spics": true,
		"wetback": true, "wetbacks": true,
	}
)

// squash collapses runs of the same letter: "niiigaaa" -> "niga".
func squash(s string) string {
	var b strings.Builder
	var last rune
	for i, r := range s {
		if i == 0 || r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}

// blocked reports whether text contains one of the filtered slurs.
func blocked(text string) bool {
	t := leet.Replace(strings.ToLower(text))
	letters := nonLetters.ReplaceAllString(t, "")
	for _, re := range blockedRuns {
		if re.MatchString(letters) {
			return true
		}
	}
	for _, word := range nonLetters.Split(t, -1) {
		if blockedWords[squash(word)] {
			return true
		}
	}
	return false
}

const blockedMessage = "that message contains a word that isn't allowed here"
