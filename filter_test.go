package main

import "testing"

func TestBlocked(t *testing.T) {
	block := []string{
		"nigger",
		"NIGGA please",
		"you niggas",
		"n1gg3r",
		"n i g g e r",
		"n.i.g.g.a",
		"niiiggggaaa",
		"nigguh",
		"niga",
		"faggot",
		"f a g g o t",
		"fagg0ts",
		"kike",
		"spics",
		"wetback",
	}
	allow := []string{
		"",
		"gg ez",
		"Niger is a country",
		"I went to Nigeria",
		"stop sniggering",
		"that was a snigger",
		"big gamer moment",
		"spicy noodles",
		"a raccoon",
		"Jiggle physics",
		"tiger king",
		"fag", // left alone on purpose: it also means a cigarette
		"what the fuck is this shit",
		"MAAAAARRRRRRK!!!",
		"bigger and bigger",
		"digging a hole",
	}
	for _, s := range block {
		if !blocked(s) {
			t.Errorf("blocked(%q) = false, want true", s)
		}
	}
	for _, s := range allow {
		if blocked(s) {
			t.Errorf("blocked(%q) = true, want false", s)
		}
	}
}
