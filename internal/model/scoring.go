package model

import (
	"sort"
)

// Dropped gibt die Indizes der gestrichenen Noten (beste + schlechteste)
// zurück. Bei mehreren gleichen Werten wird je der erste Vorkommende gestrichen;
// die höchste Note wird (nach Entfernung der niedrigsten) erneut gesucht,
// sodass bei Gleichheit zweier Maximalnoten nur eine gestrichen wird.
func Dropped(values []int) (lowIdx, highIdx int) {
	lo := -1
	hi := -1
	for i, v := range values {
		if lo == -1 || v < values[lo] {
			lo = i
		}
	}
	for i, v := range values {
		if i == lo {
			continue
		}
		if hi == -1 || v > values[hi] {
			hi = i
		}
	}
	return lo, hi
}

// TotalOf summiert die verbleibenden Noten nach dem Streichen der besten
// und schlechtesten Note. complete ist true, wenn genau numJudges Noten
// vorhanden sind.
func TotalOf(values []int, numJudges int) (total int, complete bool) {
	if len(values) != numJudges {
		return 0, false
	}
	lo, hi := Dropped(values)
	for i, v := range values {
		if i != lo && i != hi {
			total += v
		}
	}
	return total, true
}

// sortedDesc sortiert eine Kopie der Noten absteigend.
func sortedDesc(values []int) []int {
	v := append([]int(nil), values...)
	sort.Sort(sort.Reverse(sort.IntSlice(v)))
	return v
}

// CompareFull vergleicht zwei komplette Notensätze (je numJudges Noten)
// für den Gleichstand: zuerst die höchste (gestrichene) Note, dann alle
// Jurorennoten. Ergebnis: -1 (a besser), 0 (identisch), +1 (b besser).
func CompareFull(a, b []int) int {
	aa := sortedDesc(a)
	bb := sortedDesc(b)
	for i := range aa {
		if aa[i] != bb[i] {
			if aa[i] > bb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// RankEntry ist ein Teilnehmer mit kompletter Wertung.
type RankEntry struct {
	ID      int64
	Scores  []int
	Starred bool
}

// RankedEntry ist ein Ranglisteneintrag.
type RankedEntry struct {
	Entry RankEntry
	Total int
	Rank  int
}

// Rank rechnet die Platzierung: absteigend nach Summe, Gleichstand über
// CompareFull (alle Einzelnoten). Bei weiterem Gleichstand gleicher Platz,
// der nächste Platz wird übersprungen (1,1,3).
func Rank(entries []RankEntry) []RankedEntry {
	type keyed struct {
		e     RankEntry
		total int
	}
	ks := make([]keyed, 0, len(entries))
	for _, e := range entries {
		total, _ := TotalOf(e.Scores, len(e.Scores))
		ks = append(ks, keyed{e, total})
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if ks[i].total != ks[j].total {
			return ks[i].total > ks[j].total
		}
		return CompareFull(ks[i].e.Scores, ks[j].e.Scores) < 0
	})
	out := make([]RankedEntry, 0, len(ks))
	prevTotal := -1
	var prevScores []int
	for pos, k := range ks {
		rank := pos + 1
		if k.total == prevTotal && prevScores != nil && CompareFull(k.e.Scores, prevScores) == 0 {
			rank = out[len(out)-1].Rank
		}
		out = append(out, RankedEntry{Entry: k.e, Total: k.total, Rank: rank})
		prevTotal, prevScores = k.total, k.e.Scores
	}
	return out
}

// ScoresByJudge ordnet die Noten eines Teilnehmers nach Richter (1..n)
// und liefert zusätzlich, welche Indizes gestrichen werden (nur bei 7/7).
func ScoresByJudge(s []Score, numJudges int) (values []int, lowIdx, highIdx int, complete bool) {
	values = make([]int, 0, len(s))
	byJudge := make(map[int]Score, len(s))
	for _, sc := range s {
		if sc.Judge >= 1 && sc.Judge <= numJudges {
			byJudge[sc.Judge] = sc
		}
	}
	for j := 1; j <= numJudges; j++ {
		if sc, ok := byJudge[j]; ok {
			values = append(values, sc.Value)
		}
	}
	if len(values) == numJudges {
		lowIdx, highIdx = Dropped(values)
		complete = true
	}
	return values, lowIdx, highIdx, complete
}
