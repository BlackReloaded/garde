// Package schedule rechnet die Live-Zeitprognose:
// wann jeder Tanz an der Reihe ist und wann der Block endet.
//
// Grundlage:
//   - Default-Slot = Aufritt + max. Tanzlänge (BDK) + Punktezeit (Default)
//   - Beobachteter Slot = Abstand der letzten (7.) Note zwischen zwei
//     aufeinanderfolgenden, fertig bewerteten Tänzen (ein voller Zyklus)
//   - Beobachtete Punktezeit = letzte minus erste Note pro Tanz
//
// Sobald Beobachtungen vorliegen, gewinnt der gemessene Takt des Tages;
// andernfalls wird mit den Defaultwerten gerechnet.
package schedule

import (
	"time"

	"tuniert/internal/model"
)

// Settings sind die pro Disziplin konfigurierbaren Zeiten.
type Settings struct {
	MaxDanceSec   int
	ScoringDefSec int
	WalkOnSec     int
	NumJudges     int
}

// ResultEntry ist die Prognose für einen einzelnen Teilnehmer.
type ResultEntry struct {
	ID        int64
	StartNr   string
	Name      string
	Verein    string
	Starred   bool
	NumScores int
	State     model.State
	// StartEst = geschätztes Slot-Beginn (Aufritt), null wenn unbekannt/fertig.
	StartEst *time.Time
	// EndEst = geschätzter Zeitpunkt der 7. Note.
	EndEst *time.Time
	// IsCurrent = erster Teilnehmer, der noch nicht fertig ist.
	IsCurrent bool
}

// Result ist das Gesamtergebnis der Prognose.
type Result struct {
	Now     time.Time
	Entries []ResultEntry
	// SlotSec ist der aktuelle Slot-Schätzwert (Sekunden pro Tanz).
	SlotSec int
	// ScoringSec ist der aktuelle Schätzwert für die reine Punktezeit.
	ScoringSec int
	// BlockEnd ist die geschätzte Fertigstellung des letzten Tänzers.
	BlockEnd *time.Time
	// Finished ist true, wenn alle Teilnehmer fertig bewertet sind.
	Finished bool
	// Current zeigt auf den gerade an der Reihe befindlichen Teilnehmer.
	Current *ResultEntry
	// Next ist der Teilnehmer nach dem aktuellen.
	Next *ResultEntry
}

// Compute rechnet die Prognose aus den gegebenen (sortierten, nicht
// zurückgezogenen) Teilnehmern.
func Compute(set Settings, participants []model.ParticipantInfo, now time.Time) Result {
	entries := make([]ResultEntry, 0, len(participants))
	for _, p := range participants {
		st := model.StateComplete
		switch {
		case len(p.Scores) == 0:
			st = model.StateWaiting
		case len(p.Scores) < set.NumJudges:
			st = model.StateScoring
		}
		entries = append(entries, ResultEntry{
			ID: p.ID, StartNr: p.StartNr, Name: p.Name, Verein: p.Verein,
			Starred: p.Starred, NumScores: len(p.Scores), State: st,
		})
	}

	n := set.NumJudges
	scoringObs, slotObs := 0.0, 0.0
	ns, nslot := 0, 0
	var prevLast *time.Time

	for i, p := range participants {
		first, last, has := firstLast(p.Scores)
		// Punktezeit: nur aus fertig bewerteten Tänzern messbar
		// (bei Teilnoten wäre last-first nur ein Teilstück).
		if has && len(p.Scores) == n {
			scoringObs += last.Sub(first).Seconds()
			ns++
		}
		// Slot-Zyklus zwischen zwei aufeinanderfolgenden Fertigen
		if has && len(p.Scores) == n {
			if prevLast != nil {
				slotObs += last.Sub(*prevLast).Seconds()
				nslot++
			}
			prevLast = &last
		}
		_ = i
	}

	scoringSec := set.ScoringDefSec
	if ns > 0 {
		if v := int(scoringObs / float64(ns)); v > 0 {
			scoringSec = v
		}
	}
	// Mindestens 30 s Punktezeit, sonst wird die Prognose absurd.
	if scoringSec < 30 {
		scoringSec = 30
	}

	slotSec := set.WalkOnSec + set.MaxDanceSec + scoringSec
	if nslot > 0 {
		if v := int(slotObs / float64(nslot)); v > 0 {
			slotSec = v
		}
	}
	if slotSec < 60 {
		slotSec = 60
	}

	res := Result{Now: now, Entries: entries, SlotSec: slotSec, ScoringSec: scoringSec}

	// Ersten nicht fertigen Teilnehmer finden.
	curIdx := -1
	for i, e := range entries {
		if e.State != model.StateComplete {
			curIdx = i
			break
		}
	}
	if curIdx == -1 {
		res.Finished = true
		// Block-Ende = letzte tatsächlich gemessene 7. Note.
		for i := len(entries) - 1; i >= 0; i-- {
			p := participants[i]
			if first, last, ok := firstLast(p.Scores); ok {
				_ = first
				t := last
				res.BlockEnd = &t
				break
			}
		}
		// EndEst für alle fertigen = letzte Note.
		for i := range entries {
			if _, last, ok := firstLast(participants[i].Scores); ok {
				t := last
				entries[i].EndEst = &t
			}
		}
		res.Entries = entries
		return res
	}

	// Ende des aktuellen Slots.
	var endCurrent time.Time
	switch {
	case entries[curIdx].NumScores > 0:
		// Wird bereits bewertet: Rest der Punktezeit anteilig.
		rest := float64(scoringSec) * float64(n-entries[curIdx].NumScores) / float64(n)
		endCurrent = now.Add(time.Duration(rest) * time.Second)
	default:
		// Noch nicht angetreten: kompletter Slot ab jetzt.
		endCurrent = now.Add(time.Duration(slotSec) * time.Second)
	}
	entries[curIdx].IsCurrent = true
	if entries[curIdx].NumScores == 0 {
		t := now
		entries[curIdx].StartEst = &t
	}
	t := endCurrent
	entries[curIdx].EndEst = &t
	res.Current = &entries[curIdx]
	if curIdx+1 < len(entries) {
		res.Next = &entries[curIdx+1]
	}

	// Folgende Teilnehmer: jedes Slot weiter.
	for k := curIdx + 1; k < len(entries); k++ {
		off := time.Duration(k-curIdx) * time.Duration(slotSec) * time.Second
		startK := endCurrent.Add(off - time.Duration(slotSec)*time.Second)
		endK := endCurrent.Add(off)
		entries[k].StartEst = &startK
		entries[k].EndEst = &endK
	}
	res.Entries = entries
	last := entries[len(entries)-1].EndEst
	if last != nil {
		t := *last
		res.BlockEnd = &t
	}
	return res
}

func firstLast(scores []model.ScoreAt) (first, last time.Time, ok bool) {
	if len(scores) == 0 {
		return time.Time{}, time.Time{}, false
	}
	first = scores[0].At
	last = scores[0].At
	for _, s := range scores[1:] {
		if s.At.Before(first) {
			first = s.At
		}
		if s.At.After(last) {
			last = s.At
		}
	}
	return first, last, true
}
