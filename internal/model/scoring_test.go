package model

import (
	"testing"
)

func TestDropped(t *testing.T) {
	cases := []struct {
		name   string
		values []int
		low    int
		high   int
	}{
		{"normal", []int{80, 90, 70, 95, 85, 60, 75}, 5, 3},
		{"min doppelt", []int{60, 90, 60, 95, 85, 70, 75}, 0, 3},
		{"max doppelt", []int{80, 95, 70, 95, 85, 60, 75}, 5, 1},
		{"alle gleich", []int{80, 80, 80, 80, 80, 80, 80}, 0, 1},
		{"min und max gleich nicht möglich", []int{100, 0, 50, 60, 70, 80, 90}, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := Dropped(c.values)
			if lo != c.low || hi != c.high {
				t.Fatalf("Dropped(%v) = (%d,%d), want (%d,%d)", c.values, lo, hi, c.low, c.high)
			}
			if lo == hi {
				t.Fatal("low und high dürfen nicht dieselbe Note sein")
			}
		})
	}
}

func TestTotalOf(t *testing.T) {
	// 80,90,70,95,85,60,75 -> streichen 95 (hi) und 60 (lo) -> 80+90+70+85+75 = 400
	total, complete := TotalOf([]int{80, 90, 70, 95, 85, 60, 75}, 7)
	if !complete || total != 400 {
		t.Fatalf("TotalOf = (%d,%v), want (400,true)", total, complete)
	}
	if _, complete := TotalOf([]int{80, 90, 70, 95, 85, 60}, 7); complete {
		t.Fatal("6 von 7 Noten dürfen nicht 'complete' sein")
	}
}

func TestCompareFull(t *testing.T) {
	// Gleiche Summe (400), aber a hat die bessere gestrichene Maxnote:
	// a: 96,90,80,75,74,70,15 -> mittlere 5: 90+80+75+74+70 = 389? nicht relevant,
	// CompareFull vergleicht alle 7 absteigend.
	a := []int{96, 90, 80, 75, 74, 70, 15}
	b := []int{95, 90, 80, 75, 74, 70, 26}
	if got := CompareFull(a, b); got != -1 {
		t.Fatalf("CompareFull = %d, want -1 (a besser)", got)
	}
	if got := CompareFull(b, a); got != 1 {
		t.Fatalf("CompareFull = %d, want +1 (b schlechter)", got)
	}
	c := []int{96, 90, 80, 75, 74, 70, 15}
	if got := CompareFull(c, a); got != 0 {
		t.Fatalf("CompareFull = %d, want 0 (identisch)", got)
	}
	// Reihenfolge ist egal (b = {95, 90, 80, 75, 74, 70, 26}):
	d := []int{26, 70, 74, 75, 80, 90, 95}
	if got := CompareFull(d, b); got != 0 {
		t.Fatalf("CompareFull = %d, want 0 (permutation)", got)
	}
}

func TestRank(t *testing.T) {
	entries := []RankEntry{
		{ID: 1, Scores: []int{80, 90, 70, 95, 85, 60, 75}}, // 400
		{ID: 2, Scores: []int{82, 91, 71, 95, 84, 60, 75}}, // 403 (besser)
		{ID: 3, Scores: []int{80, 90, 70, 95, 85, 60, 75}}, // 400, identisch mit 1
		{ID: 4, Scores: []int{70, 80, 60, 85, 75, 50, 65}}, // 350
	}
	// ID2 führt mit 403. ID1 und ID3 haben beide 400 und identische
	// Einzelnoten -> gleicher Platz 2. ID4 kommt als 4.
	ranked := Rank(entries)
	if len(ranked) != 4 {
		t.Fatalf("len = %d, want 4", len(ranked))
	}
	// Erwartet: 2. Platz für ID 2, dann 1 und 3 gleich auf (beide 400, identische Noten) -> Ränge 2,3?
	// Sortierung: ID2 (400, beste), dann ID1 und ID3 (400, identisch) mit stabilem Order, dann ID4.
	want := []struct {
		id   int64
		rank int
	}{
		{2, 1},
		{1, 2},
		{3, 2},
		{4, 4},
	}
	for i, w := range want {
		if ranked[i].Entry.ID != w.id || ranked[i].Rank != w.rank {
			t.Fatalf("Position %d: got (id=%d, rank=%d), want (id=%d, rank=%d)",
				i, ranked[i].Entry.ID, ranked[i].Rank, w.id, w.rank)
		}
	}
}
