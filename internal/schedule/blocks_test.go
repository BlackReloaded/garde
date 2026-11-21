package schedule

import (
	"fmt"
	"testing"
	"time"

	"turnier/internal/model"
)

func mkDisc(id int64, name string) model.Discipline {
	return model.Discipline{ID: id, Name: name, MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7}
}

// wantSeg prüft, dass Plan[i] die erwarteten ID/Name/Art und Zeiten hat.
func wantSeg(t *testing.T, plan []PlanSegment, i int, id int64, name, kind string, s, e time.Time) {
	t.Helper()
	if i >= len(plan) {
		t.Fatalf("Plan hat nur %d Abschnitte, Index %d erwartet", len(plan), i)
	}
	seg := plan[i]
	if seg.ID != id || seg.Name != name || seg.Kind != kind {
		t.Fatalf("Plan[%d] = %d %q %q, want %d %q %q", i, seg.ID, seg.Name, seg.Kind, id, name, kind)
	}
	if !seg.StartEst.Equal(s) || !seg.EndEst.Equal(e) {
		t.Fatalf("Plan[%d] (%s) = %v–%v, want %v–%v", i, name, seg.StartEst, seg.EndEst, s, e)
	}
}

// at baut einen Zeitpunkt am 15.03.2026 (Anzeigzone) aus "HH:MM".
func at(hm string) time.Time {
	var h, m int
	fmt.Sscanf(hm, "%d:%02d", &h, &m)
	return time.Date(2026, 3, 15, h, m, 0, 0, Zone)
}

// act baut die Messbasis: total Tänzer, davon done fertig; first = erste
// Note insgesamt, last = letzte Note eines fertigen Tanzes ("" = keiner).
func act(total, done int, first, last string) DiscActual {
	a := DiscActual{Total: total, Done: done}
	if first != "" {
		a.First = at(first)
	}
	if last != "" {
		a.Last = at(last)
	}
	return a
}

func TestPlanWithBlocks(t *testing.T) {
	// Start 09:00, keine feste Pause zwischen den Abschnitten, je 1 Tänzer
	// pro Disziplin (5-min-Slots). Mittagspause (30 min) nach Disziplin 1,
	// Siegerehrung (15 min) am Ende. Ohne Messungen = reine Planrechnung.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Solo"), mkDisc(3, "Paare")}
	acts := map[int64]DiscActual{1: act(1, 0, "", ""), 2: act(1, 0, "", ""), 3: act(1, 0, "", "")}
	blocks := []model.PlannedBlock{
		{ID: 101, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60},
		{ID: 102, Name: "Siegerehrung", Position: -1, DurationSec: 15 * 60},
	}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 5 {
		t.Fatalf("Plan-Länge = %d, want 5", len(plan))
	}
	wantSeg(t, plan, 0, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 0, 0, 0, Zone), time.Date(2026, 3, 15, 9, 5, 0, 0, Zone))
	// direkt danach -> Mittagspause 09:05–09:35.
	wantSeg(t, plan, 1, 101, "Mittagspause", SegmentBlock,
		time.Date(2026, 3, 15, 9, 5, 0, 0, Zone), time.Date(2026, 3, 15, 9, 35, 0, 0, Zone))
	// direkt danach -> Solo 09:35–09:40.
	wantSeg(t, plan, 2, 2, "Solo", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 35, 0, 0, Zone), time.Date(2026, 3, 15, 9, 40, 0, 0, Zone))
	// direkt danach -> Paare 09:40–09:45.
	wantSeg(t, plan, 3, 3, "Paare", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 40, 0, 0, Zone), time.Date(2026, 3, 15, 9, 45, 0, 0, Zone))
	// direkt danach -> Siegerehrung 09:45–10:00.
	wantSeg(t, plan, 4, 102, "Siegerehrung", SegmentBlock,
		time.Date(2026, 3, 15, 9, 45, 0, 0, Zone), time.Date(2026, 3, 15, 10, 0, 0, 0, Zone))
}

func TestPlanBlockAtStart(t *testing.T) {
	// Block am Anfang (Position 0) kommt zuerst; die erste Disziplin
	// schließt direkt daran an (keine feste Pause).
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	acts := map[int64]DiscActual{1: act(1, 0, "", "")}
	blocks := []model.PlannedBlock{{ID: 201, Name: "Eröffnung", Position: 0, DurationSec: 5 * 60}}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 2 {
		t.Fatalf("Plan-Länge = %d, want 2", len(plan))
	}
	// Eröffnung 09:00–09:05.
	wantSeg(t, plan, 0, 201, "Eröffnung", SegmentBlock,
		time.Date(2026, 3, 15, 9, 0, 0, 0, Zone), time.Date(2026, 3, 15, 9, 5, 0, 0, Zone))
	// direkt danach -> Garde 09:05–09:10.
	wantSeg(t, plan, 1, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 5, 0, 0, Zone), time.Date(2026, 3, 15, 9, 10, 0, 0, Zone))
}

func TestPlanBlockPositionClampedToEnd(t *testing.T) {
	// Position jenseits der Disziplinanzahl (hier 7 bei 2 Disziplinen)
	// gilt als "am Ende".
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Paare")}
	acts := map[int64]DiscActual{1: act(1, 0, "", ""), 2: act(1, 0, "", "")}
	blocks := []model.PlannedBlock{{ID: 301, Name: "Siegerehrung", Position: 7, DurationSec: 5 * 60}}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	wantSeg(t, plan, 2, 301, "Siegerehrung", SegmentBlock,
		time.Date(2026, 3, 15, 9, 10, 0, 0, Zone), time.Date(2026, 3, 15, 9, 15, 0, 0, Zone))
}

func TestPlanBlockAfterEmptyDiscipline(t *testing.T) {
	// Leere Disziplin (Solo) wird übersprungen; der Block an ihrer
	// Position (2) erscheint trotzdem – die Abschnitte laufen direkt
	// aneinander (keine feste Pause).
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Solo"), mkDisc(3, "Paare")}
	acts := map[int64]DiscActual{1: act(1, 0, "", ""), 2: act(0, 0, "", ""), 3: act(1, 0, "", "")}
	blocks := []model.PlannedBlock{{ID: 401, Name: "Pause", Position: 2, DurationSec: 10 * 60}}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	wantSeg(t, plan, 0, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 0, 0, 0, Zone), time.Date(2026, 3, 15, 9, 5, 0, 0, Zone))
	wantSeg(t, plan, 1, 401, "Pause", SegmentBlock,
		time.Date(2026, 3, 15, 9, 5, 0, 0, Zone), time.Date(2026, 3, 15, 9, 15, 0, 0, Zone))
	wantSeg(t, plan, 2, 3, "Paare", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 15, 0, 0, Zone), time.Date(2026, 3, 15, 9, 20, 0, 0, Zone))
}

func TestPlanBlockZeroDurationSkipped(t *testing.T) {
	// Block ohne Dauer (0) wird nicht eingeplant.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	acts := map[int64]DiscActual{1: act(1, 0, "", "")}
	blocks := []model.PlannedBlock{{ID: 501, Name: "Platzhalter", Position: -1, DurationSec: 0}}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 1 {
		t.Fatalf("Plan-Länge = %d, want 1", len(plan))
	}
	if plan[0].Kind != SegmentDiscipline {
		t.Fatalf("Plan[0].Kind = %q, want Disziplin", plan[0].Kind)
	}
}

func TestPlanNoStartWithBlocks(t *testing.T) {
	// Ohne gesetzte Startzeit gibt es keine Plan-Basis – auch nicht mit
	// Blöcken (diese hängen an der Turnier-Startzeit).
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	blocks := []model.PlannedBlock{{ID: 601, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60}}
	if plan := PlanTimeline(0, now, Zone, discs, blocks, nil); plan != nil {
		t.Fatalf("Plan = %v, want nil (keine Startzeit gesetzt)", plan)
	}
}

func TestPlanNoDiscsNoBlocks(t *testing.T) {
	// Startzeit gesetzt, aber keine Disziplinen UND keine Blöcke → nil.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, Zone)
	if plan := PlanTimeline(9*3600, now, Zone, nil, nil, nil); plan != nil {
		t.Fatalf("Plan = %v, want nil", plan)
	}
}

func TestPlanTimelineRealAnchoring(t *testing.T) {
	// Der gemeldete Fall: Es ist schon 11:53, aber die Streife zeigt noch
	// die Planzeiten vom Morgen (Pause "11:25" – längst vorbei). Neu:
	// Fertige Abschnitte zeigen ihre gemessenen Zeiten, die Pause endet
	// mit der echten Wiederkehr, der Rest rechnet ab der Realität weiter.
	now := at("11:53")
	discs := []model.Discipline{mkDisc(1, "Jugend Garde"), mkDisc(2, "Junioren Solo")}
	blocks := []model.PlannedBlock{{ID: 901, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60}}
	acts := map[int64]DiscActual{
		// Garde fertig: erste Note 09:02, letzte 09:22.
		1: act(4, 4, "09:02", "09:22"),
		// Solo läuft: erste Note 11:20, 3 von 10 fertig, letzte 11:50.
		2: act(10, 3, "11:20", "11:50"),
	}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	// Garde: echte Zeit statt Plan 09:00–09:20.
	wantSeg(t, plan, 0, 1, "Jugend Garde", SegmentDiscipline, at("09:02"), at("09:22"))
	// Pause: von Garde-Ende bis Solos erster Note (echte Wiederkehr).
	wantSeg(t, plan, 1, 901, "Mittagspause", SegmentBlock, at("09:22"), at("11:20"))
	// Solo: echter Start; Ende-Prognose 11:50 + 7 Tänzer × 5 min = 12:25.
	wantSeg(t, plan, 2, 2, "Junioren Solo", SegmentDiscipline, at("11:20"), at("12:25"))
}

func TestPlanTimelineGapWithoutRecovery(t *testing.T) {
	// Pause läuft bzw. Leerlauf: Nach der Pause hat noch niemand wieder
	// angefangen – die Pause behält ihre Plan-Dauer ab dem realen
	// Disziplin-Ende, die nächste Disziplin rechnet daran weiter.
	now := at("11:53")
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Solo")}
	blocks := []model.PlannedBlock{{ID: 901, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60}}
	acts := map[int64]DiscActual{
		1: act(4, 4, "09:02", "09:22"),
		2: act(10, 0, "", ""), // noch nicht begonnen
	}
	plan := PlanTimeline(9*3600, now, Zone, discs, blocks, acts)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	wantSeg(t, plan, 1, 901, "Mittagspause", SegmentBlock, at("09:22"), at("09:52"))
	// Solo (10 Tänzer × 5 min) ab Pause-Ende.
	wantSeg(t, plan, 2, 2, "Solo", SegmentDiscipline, at("09:52"), at("10:42"))
}

func TestActualFromParticipants(t *testing.T) {
	score := func(min int, judge int) model.Score {
		return model.Score{Judge: judge, Value: 80, UpdatedAt: at(fmt.Sprintf("%02d:%02d", 9+min/60, min%60))}
	}
	parts := []model.Participant{
		// Fertig bewerteter Tanz: 7 Noten, 09:00–09:12.
		{ID: 1, Scores: []model.Score{score(0, 1), score(2, 2), score(4, 3), score(6, 4), score(8, 5), score(10, 6), score(12, 7)}},
		// Zurückgezogen – zählt nicht, auch nicht für First.
		{ID: 2, Withdrawn: true, Scores: []model.Score{score(1, 1)}},
		// Teilbewerteter Tanz: 2 Noten, früheste 09:30.
		{ID: 3, Scores: []model.Score{score(30, 1), score(35, 2)}},
	}
	a := ActualFromParticipants(7, parts)
	if a.Total != 2 || a.Done != 1 {
		t.Fatalf("Total/Done = %d/%d, want 2/1", a.Total, a.Done)
	}
	if !a.First.Equal(at("09:00")) {
		t.Fatalf("First = %v, want 09:00", a.First)
	}
	if !a.Last.Equal(at("09:12")) {
		t.Fatalf("Last = %v, want 09:12 (nur aus fertigem Tanz)", a.Last)
	}
}
