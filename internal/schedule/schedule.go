// Package schedule rechnet die Live-Zeitprognose:
// wann jeder Tanz an der Reihe ist und wann der Block endet.
//
// Grundlage (gemessen "von Bewertung zur nächsten Bewertung"):
//   - Gap = Ende einer Bewertung (letzte Note) bis Beginn der nächsten
//     Bewertung (erste Note) – schon mit der ersten Note des Nächsten
//     messbar, nicht erst nach dessen 7. Note
//   - Punktezeit = letzte minus erste Note pro fertigem Tanz
//   - Slot-Zyklus = Gap + Punktezeit
//
// Für die Prognose wird die aktuelle Durchschnittszeit der gesammelten
// Beobachtungen genutzt; solange noch nicht genug Daten vorliegen,
// greifen die Defaultwerte (Gap = Aufritt + max. Tanzlänge,
// Punktezeit = Default).
package schedule

import (
	"time"

	"turnier/internal/model"
)

// gapCapSec: größter plausibler Abstand "von Bewertung zur nächsten
// Bewertung" (40 min). Größere Abstände gelten als Pause und fließen
// nicht in die Durchschnittszeit ein.
const gapCapSec = 40 * 60

// startRolloverWindow: Wie spät darf es noch sein, wenn die gesetzte
// Uhrzeit (z. B. 00:30) heute schon vorbei ist und damit auf den
// Folgetag gedeutet? Innerhalb des Fensters (nächste Ankunft der
// Uhrzeit ≤ 6 h) ist ein Start über Mitternacht gemeint. Davor gilt:
// der Start ist heute gelaufen, kein Rollover.
const startRolloverWindow = 6 * time.Hour

// Settings sind die pro Disziplin konfigurierbaren Zeiten.
type Settings struct {
	MaxDanceSec   int
	ScoringDefSec int
	WalkOnSec     int
	NumJudges     int
	// AnchorAt ist die geplante Startzeit der Disziplin (aus der
	// Turnier-Zeitplanung). Solange noch keine einzige Note existiert,
	// startet die Prognose pünktlich zu dieser Zeit statt "ab jetzt" –
	// aber nur wenn sie in der Zukunft liegt. Die erste eingetragene
	// Note verankert die Prognose dann in der Realität.
	AnchorAt *time.Time
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
	// SlotSec ist der aktuelle Slot-Schätzwert (Sekunden pro Tanzzyklus,
	// = Gap "von Bewertung zur nächsten" + Punktezeit).
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

// Compute rechnet die Prognose aus den gegebenen Teilnehmern, sortiert
// nach Startnummer (aufsteigend), ohne zurückgezogene.
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
	scoringObs, gapObs := 0.0, 0.0
	ns, ngap := 0, 0

	for i, p := range participants {
		first, last, has := firstLast(p.Scores)
		// Punktezeit: nur aus fertig bewerteten Tänzern messbar
		// (bei Teilnoten wäre last-first nur ein Teilstück).
		if has && len(p.Scores) == n {
			scoringObs += last.Sub(first).Seconds()
			ns++
		}
		// Gap "von Bewertung zur nächsten Bewertung": Ende der Bewertung
		// des Vorturners (letzte Note) bis Beginn dieser (erste Note).
		// Schon mit der ersten Note messbar – nicht erst nach der 7.
		// Abnormal lange Abstände (Pause) fließen nicht in den Schnitt.
		if has && i > 0 {
			prev := participants[i-1]
			if len(prev.Scores) == n {
				if _, prevLast, _ := firstLast(prev.Scores); first.After(prevLast) && first.Sub(prevLast).Seconds() <= gapCapSec {
					gapObs += first.Sub(prevLast).Seconds()
					ngap++
				}
			}
		}
	}

	// Punktezeit: aktuelle Durchschnittszeit aller fertig bewerteten
	// Tänze; ohne Beobachtungen der Defaultwert.
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

	// Gap "von Bewertung zur nächsten Bewertung": aktuelle
	// Durchschnittszeit; ohne Beobachtungen der Defaultwert
	// (Aufritt + max. Tanzlänge).
	gapSec := set.WalkOnSec + set.MaxDanceSec
	if ngap > 0 {
		if v := int(gapObs / float64(ngap)); v > 0 {
			gapSec = v
		}
	}

	// Slot-Zyklus = Gap + Punktezeit (ein voller Tanzzyklus).
	slotSec := gapSec + scoringSec
	if slotSec < 60 {
		slotSec = 60
	}

	res := Result{Now: now, Entries: entries, SlotSec: slotSec, ScoringSec: scoringSec}

	// Anker der Prognose: Standard ist "jetzt". Ist eine geplante
	// Startzeit (AnchorAt) gesetzt, liegt sie in der Zukunft und existiert
	// noch keine einzige Note, startet der Zeitplan pünktlich – die erste
	// eingetragene Note verankert die Prognose dann in der Realität.
	anchor := now
	if set.AnchorAt != nil && !set.AnchorAt.Before(now) {
		anyScore := false
		for _, p := range participants {
			if len(p.Scores) > 0 {
				anyScore = true
				break
			}
		}
		if !anyScore {
			anchor = *set.AnchorAt
		}
	}

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
		// (Hier ist anchor == now, denn Noten schalten den Plan-Anchor ab.)
		rest := float64(scoringSec) * float64(n-entries[curIdx].NumScores) / float64(n)
		endCurrent = anchor.Add(time.Duration(rest) * time.Second)
	default:
		// Noch nicht angetreten: kompletter Slot ab Anker
		// (geplante Startzeit, sonst jetzt).
		endCurrent = anchor.Add(time.Duration(slotSec) * time.Second)
	}
	entries[curIdx].IsCurrent = true
	if entries[curIdx].NumScores == 0 {
		t := anchor
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

// PlanStartAt rechnet den konkreten Zeitpunkt der Turnier-Startzeit
// (Sekunden seit Mitternacht) relativ zu now: normal die heutige
// Ankunft der Uhrzeit. Ist die heutige Ankunft bereits vorbei, die
// nächste (morgen) aber innerhalb von startRolloverWindow, ist ein
// Start über Mitternacht gemeint (jetzt 23:00, Start 00:30 → morgen
// 00:30) – dann wird der Folgetag genommen.
func PlanStartAt(startAtSec int, now time.Time) time.Time {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	t := midnight.Add(time.Duration(startAtSec) * time.Second)
	if t.Before(now) && t.Add(24*time.Hour).Sub(now) <= startRolloverWindow {
		t = t.Add(24 * time.Hour)
	}
	return t
}

// Arten eines Plan-Abschnitts.
const (
	SegmentDiscipline = "disziplin"
	SegmentBlock      = "block"
)

// PlanSegment ist ein geplanter Abschnitt des Turnier-Plans: eine
// Disziplin (ID = Disziplin-ID) oder ein eingeplanter Block wie
// Mittagspause/Siegerehrung (ID = Block-ID).
type PlanSegment struct {
	ID       int64
	Name     string
	Kind     string // SegmentDiscipline oder SegmentBlock
	StartEst *time.Time
	EndEst   *time.Time
}

// PlanDisciplineStarts rechnet den geplanten Ablauf eines Turniers:
// Turnier-Startzeit (Sekunden seit Mitternacht, s. PlanStartAt) + Dauer
// der Disziplinen (nicht zurückgezogene Teilnehmer × Slot-Default) +
// eingeplante Blöcke an ihrer Position (Mittagspause, Siegerehrung, …)
// + Pause dazwischen. Ohne gesetzte Startzeit (startAtSec <= 0) gibt es
// keine Plan-Basis (nil); ohne Disziplinen UND ohne Blöcke ebenso.
// Leere Disziplinen werden
// übersprungen – es fällt auch keine Pause rundherum an; Blöcke an
// ihrer Position erscheinen trotzdem. Die Pause gilt zwischen je zwei
// aufeinanderfolgenden Abschnitten (Disziplin–Disziplin,
// Disziplin–Block, Block–Disziplin), aber nicht vor dem ersten.
func PlanDisciplineStarts(startAtSec, breakSec int, now time.Time,
	discs []model.Discipline, activeParts map[int64]int,
	blocks []model.PlannedBlock) []PlanSegment {
	if startAtSec <= 0 || (len(discs) == 0 && len(blocks) == 0) {
		return nil
	}
	t := PlanStartAt(startAtSec, now)
	out := make([]PlanSegment, 0, len(discs)+len(blocks))
	addSeg := func(seg PlanSegment, dur time.Duration) {
		if len(out) > 0 && breakSec > 0 {
			t = t.Add(time.Duration(breakSec) * time.Second)
		}
		s := t
		e := t.Add(dur)
		seg.StartEst = &s
		seg.EndEst = &e
		out = append(out, seg)
		t = e
	}
	// addBlocks platziert alle Blöcke an der Position pos:
	// 0 = am Anfang, 1..N = nach der N-ten Disziplin, -1 = am Ende.
	// Positionen jenseits der Disziplinanzahl gelten als "am Ende".
	addBlocks := func(pos int) {
		for _, b := range blocks {
			if b.DurationSec <= 0 {
				continue
			}
			p := b.Position
			if p > len(discs) {
				p = -1
			}
			if p == pos {
				addSeg(PlanSegment{ID: b.ID, Name: b.Name, Kind: SegmentBlock},
					time.Duration(b.DurationSec)*time.Second)
			}
		}
	}
	addBlocks(0)
	for i, d := range discs {
		n := activeParts[d.ID]
		if n > 0 {
			slot := d.WalkOnSec + d.MaxDanceSec + d.ScoringDefSec
			if slot < 60 {
				slot = 60
			}
			addSeg(PlanSegment{ID: d.ID, Name: d.Name, Kind: SegmentDiscipline},
				time.Duration(n*slot)*time.Second)
		}
		addBlocks(i + 1)
	}
	addBlocks(-1)
	return out
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
