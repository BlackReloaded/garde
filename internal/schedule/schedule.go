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
	_ "time/tzdata" // Zone-Datenbank im Binary – Berlin geht auch ohne tzdata im System

	"turnier/internal/model"
)

// Zone ist die Anzeigetimezone für alle berechneten und angezeigten
// Zeiten: Turnier-Startzeiten werden in deutscher Wanduhrzeit eingegeben,
// die Anzeigen in der Live-Ansicht sind ebenso gemeint – unabhängig davon,
// ob der Rechner (z. B. Docker-Container) in UTC läuft.
var Zone = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return time.Local
	}
	return loc
}()

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
	// AnchorAt ist der geplante Anschlusszeitpunkt der Disziplin aus der
	// Turnier-Zeitplanung (PlanTimeline: gemessene Zeiten, wo vorhanden,
	// sonst Plan-Dauern). Solange noch keine einzige Note existiert,
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

// Compute rechnet die Prognose aus den gegebenen Teilnehmern – in der
// übergebenen Reihenfolge (= Startreihenfolge aus Import/Editor, nicht
// nach Startnummer sortiert), ohne zurückgezogene.
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
// (Sekunden seit Mitternacht in der Anzeigzone loc) relativ zu now:
// normal die heutige Ankunft der Uhrzeit – gemessen am Tag, wie er in loc
// läuft (der Container-Rechner darf ruhig in UTC laufen). Ist die heutige
// Ankunft bereits vorbei, die nächste (morgen) aber innerhalb von
// startRolloverWindow, ist ein Start über Mitternacht gemeint (jetzt 23:00,
// Start 00:30 → morgen 00:30) – dann wird der Folgetag genommen.
func PlanStartAt(startAtSec int, now time.Time, loc *time.Location) time.Time {
	nl := now.In(loc)
	midnight := time.Date(nl.Year(), nl.Month(), nl.Day(), 0, 0, 0, 0, loc)
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

// DiscActual hält die tatsächlich gemessenen Zeiten einer Disziplin,
// verdichtet aus den Noten-Zeitstempeln: Total/Done = aktive bzw. komplett
// bewertete Tänzer; First = früheste Note der ganzen Disziplin
// (Nullzeit = noch nicht begonnen); Last = späteste Note eines komplett
// bewerteten Tanzes (Nullzeit = noch keiner fertig).
type DiscActual struct {
	Total int
	Done  int
	First time.Time
	Last  time.Time
}

// ActualFromParticipants verdichtet die Noten-Zeitstempel der Teilnehmer
// auf die Messbasis von PlanTimeline. Zurückgezogene Tänzer zählen nicht
// (sie haben keinen Slot).
func ActualFromParticipants(numJudges int, parts []model.Participant) DiscActual {
	a := DiscActual{}
	for _, p := range parts {
		if p.Withdrawn {
			continue
		}
		a.Total++
		if len(p.Scores) == 0 {
			continue
		}
		first, last := firstLastOf(p.Scores)
		if a.First.IsZero() || first.Before(a.First) {
			a.First = first
		}
		if len(p.Scores) == numJudges {
			a.Done++
			if last.After(a.Last) {
				a.Last = last
			}
		}
	}
	return a
}

// PlanTimeline rechnet den Ablauf eines Turniers aus Plan und gemessener
// Realität – Realität schlägt Plan, überall und sofort:
//   - Fertige Disziplinen zeigen ihre gemessenen Zeiten (Start = früheste
//     Note, Ende = späteste Note eines fertigen Tanzes).
//   - Eine Pause (Block) endet, sobald die nächste Disziplin wieder Noten
//     hat: Ende = echte Wiederkehr, nicht die längst überholte Planzeit.
//   - Eine begonnene Disziplin startet real; ihr Ende ist eine Prognose
//     ab der letzten gemessenen Note.
//   - Was noch nicht begonnen hat, wird von der Plan-Dauer (Tänzer ×
//     Slot-Default bzw. Blockdauer) fortgerechnet – aber an den
//     Real-Anschluss, nicht an die Starrekette ab Wunsch-Startzeit.
//
// Ohne Messungen ergibt sich dieselbe Kette wie reine Planrechnung.
// Zwischen den Abschnitten gibt es keine feste Pause – Disziplinen und
// Blöcke laufen direkt aneinander. Ohne gesetzte Startzeit
// (startAtSec <= 0) gibt es keine Plan-Basis (nil); ohne Disziplinen UND
// ohne Blöcke ebenso. Leere Disziplinen werden übersprungen; Blöcke an
// ihrer Position erscheinen trotzdem.
func PlanTimeline(startAtSec int, now time.Time, loc *time.Location,
	discs []model.Discipline, blocks []model.PlannedBlock,
	act map[int64]DiscActual) []PlanSegment {
	if startAtSec <= 0 || (len(discs) == 0 && len(blocks) == 0) {
		return nil
	}
	// Lineare Abschnittsfolge: Disziplinen in Reihenfolge, Blöcke an
	// ihrer Position (0 = am Anfang, 1..N = nach der N-ten Disziplin,
	// -1 oder jenseits der Disziplinanzahl = am Ende).
	type seqItem struct {
		disc  *model.Discipline
		block *model.PlannedBlock
	}
	seq := make([]seqItem, 0, len(discs)+len(blocks))
	addBlocks := func(pos int) {
		for i := range blocks {
			b := &blocks[i]
			if b.DurationSec <= 0 {
				continue
			}
			p := b.Position
			if p > len(discs) {
				p = -1
			}
			if p == pos {
				seq = append(seq, seqItem{block: b})
			}
		}
	}
	addBlocks(0)
	for i := range discs {
		seq = append(seq, seqItem{disc: &discs[i]})
		addBlocks(i + 1)
	}
	addBlocks(-1)

	// Durchlauf mit Zeitcursor: Gemessenes setzt den Anschluss, die
	// Prognose der noch nicht begonnenen Abschnitte läuft von dort weiter.
	// Darum springt die Kette auch nie mehr stundenweit in die Vergangenheit.
	cursor := PlanStartAt(startAtSec, now, loc)
	out := make([]PlanSegment, 0, len(seq))
	for i, it := range seq {
		seg := PlanSegment{}
		var start, end time.Time
		if it.disc != nil {
			d := it.disc
			seg = PlanSegment{ID: d.ID, Name: d.Name, Kind: SegmentDiscipline}
			a := act[d.ID]
			if a.Total == 0 {
				continue // leere Disziplin: kein Abschnitt
			}
			slot := d.WalkOnSec + d.MaxDanceSec + d.ScoringDefSec
			if slot < 60 {
				slot = 60
			}
			switch {
			case !a.Last.IsZero() && a.Done == a.Total:
				// fertig: echte Zeiten – auch wenn sie vor dem
				// Plan liegen (Realität war schneller).
				start, end = a.First, a.Last
			case !a.First.IsZero():
				// gestartet: echter Start; Ende als Prognose ab
				// der letzten gemessenen Note über die Resttänzer.
				start = a.First
				base := a.Last
				if base.IsZero() {
					base = a.First
				}
				end = base.Add(time.Duration((a.Total-a.Done)*slot) * time.Second)
			default:
				// noch nicht gestartet: Prognose ab Anschluss.
				start = cursor
				end = cursor.Add(time.Duration(a.Total*slot) * time.Second)
			}
		} else {
			b := it.block
			seg = PlanSegment{ID: b.ID, Name: b.Name, Kind: SegmentBlock}
			start = cursor
			end = cursor.Add(time.Duration(b.DurationSec) * time.Second)
			// Pauken-Ende real, sobald die nächste Disziplin mit
			// Noten begonnen hat: das Ende ist deren erste Note –
			// die Pause war spätestens dann vorbei.
			for j := i + 1; j < len(seq); j++ {
				if seq[j].disc == nil {
					continue
				}
				if a, ok := act[seq[j].disc.ID]; ok &&
					!a.First.IsZero() && a.First.After(start) {
					end = a.First
				}
				break
			}
		}
		if !end.After(start) {
			end = start
		}
		s, e := start, end
		seg.StartEst, seg.EndEst = &s, &e
		out = append(out, seg)
		cursor = end
	}
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

// firstLastOf ist dasselbe für die Note-Typ der Store-Schicht.
func firstLastOf(scores []model.Score) (first, last time.Time) {
	first, last = scores[0].UpdatedAt, scores[0].UpdatedAt
	for _, s := range scores[1:] {
		if s.UpdatedAt.Before(first) {
			first = s.UpdatedAt
		}
		if s.UpdatedAt.After(last) {
			last = s.UpdatedAt
		}
	}
	return first, last
}
