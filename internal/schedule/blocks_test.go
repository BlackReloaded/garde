package schedule

import (
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

func TestPlanWithBlocks(t *testing.T) {
	// Start 09:00, Pause 10 min zwischen JE ZWEI Abschnitten, je 1 Tänzer
	// pro Disziplin (5-min-Slots). Mittagspause (30 min) nach Disziplin 1,
	// Siegerehrung (15 min) am Ende.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Solo"), mkDisc(3, "Paare")}
	counts := map[int64]int{1: 1, 2: 1, 3: 1}
	blocks := []model.PlannedBlock{
		{ID: 101, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60},
		{ID: 102, Name: "Siegerehrung", Position: -1, DurationSec: 15 * 60},
	}
	plan := PlanDisciplineStarts(9*3600, 10*60, now, discs, counts, blocks)
	if len(plan) != 5 {
		t.Fatalf("Plan-Länge = %d, want 5", len(plan))
	}
	wantSeg(t, plan, 0, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 5, 0, 0, time.Local))
	// + 10 min Pause -> Mittagspause 09:15–09:45.
	wantSeg(t, plan, 1, 101, "Mittagspause", SegmentBlock,
		time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 45, 0, 0, time.Local))
	// + 10 min Pause -> Solo 09:55–10:00.
	wantSeg(t, plan, 2, 2, "Solo", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 55, 0, 0, time.Local), time.Date(2026, 3, 15, 10, 0, 0, 0, time.Local))
	// + 10 min Pause -> Paare 10:10–10:15.
	wantSeg(t, plan, 3, 3, "Paare", SegmentDiscipline,
		time.Date(2026, 3, 15, 10, 10, 0, 0, time.Local), time.Date(2026, 3, 15, 10, 15, 0, 0, time.Local))
	// + 10 min Pause -> Siegerehrung 10:25–10:40.
	wantSeg(t, plan, 4, 102, "Siegerehrung", SegmentBlock,
		time.Date(2026, 3, 15, 10, 25, 0, 0, time.Local), time.Date(2026, 3, 15, 10, 40, 0, 0, time.Local))
}

func TestPlanBlockAtStart(t *testing.T) {
	// Block am Anfang (Position 0) kommt zuerst, ohne Pause davor;
	// zwischen Block und Disziplin gilt die Pause trotzdem.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	counts := map[int64]int{1: 1}
	blocks := []model.PlannedBlock{{ID: 201, Name: "Eröffnung", Position: 0, DurationSec: 5 * 60}}
	plan := PlanDisciplineStarts(9*3600, 10*60, now, discs, counts, blocks)
	if len(plan) != 2 {
		t.Fatalf("Plan-Länge = %d, want 2", len(plan))
	}
	// Eröffnung 09:00–09:05 (ohne Pause vor dem ersten Abschnitt).
	wantSeg(t, plan, 0, 201, "Eröffnung", SegmentBlock,
		time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 5, 0, 0, time.Local))
	// + 10 min Pause -> Garde 09:15–09:20.
	wantSeg(t, plan, 1, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 20, 0, 0, time.Local))
}

func TestPlanBlockPositionClampedToEnd(t *testing.T) {
	// Position jenseits der Disziplinanzahl (hier 7 bei 2 Disziplinen)
	// gilt als "am Ende".
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Paare")}
	counts := map[int64]int{1: 1, 2: 1}
	blocks := []model.PlannedBlock{{ID: 301, Name: "Siegerehrung", Position: 7, DurationSec: 5 * 60}}
	plan := PlanDisciplineStarts(9*3600, 0, now, discs, counts, blocks)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	wantSeg(t, plan, 2, 301, "Siegerehrung", SegmentBlock,
		time.Date(2026, 3, 15, 9, 10, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local))
}

func TestPlanBlockAfterEmptyDiscipline(t *testing.T) {
	// Leere Disziplin (Solo) wird übersprungen; der Block an ihrer
	// Position (2) erscheint trotzdem – ohne Pause rund um die Lücke.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde"), mkDisc(2, "Solo"), mkDisc(3, "Paare")}
	counts := map[int64]int{1: 1, 3: 1} // Solo leer
	blocks := []model.PlannedBlock{{ID: 401, Name: "Pause", Position: 2, DurationSec: 10 * 60}}
	plan := PlanDisciplineStarts(9*3600, 0, now, discs, counts, blocks)
	if len(plan) != 3 {
		t.Fatalf("Plan-Länge = %d, want 3", len(plan))
	}
	wantSeg(t, plan, 0, 1, "Garde", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 5, 0, 0, time.Local))
	wantSeg(t, plan, 1, 401, "Pause", SegmentBlock,
		time.Date(2026, 3, 15, 9, 5, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local))
	wantSeg(t, plan, 2, 3, "Paare", SegmentDiscipline,
		time.Date(2026, 3, 15, 9, 15, 0, 0, time.Local), time.Date(2026, 3, 15, 9, 20, 0, 0, time.Local))
}

func TestPlanBlockZeroDurationSkipped(t *testing.T) {
	// Block ohne Dauer (0) wird nicht eingeplant.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	counts := map[int64]int{1: 1}
	blocks := []model.PlannedBlock{{ID: 501, Name: "Platzhalter", Position: -1, DurationSec: 0}}
	plan := PlanDisciplineStarts(9*3600, 0, now, discs, counts, blocks)
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
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	discs := []model.Discipline{mkDisc(1, "Garde")}
	counts := map[int64]int{1: 1}
	blocks := []model.PlannedBlock{{ID: 601, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60}}
	if plan := PlanDisciplineStarts(0, 0, now, discs, counts, blocks); plan != nil {
		t.Fatalf("Plan = %v, want nil (keine Startzeit gesetzt)", plan)
	}
}

func TestPlanNoDiscsNoBlocks(t *testing.T) {
	// Startzeit gesetzt, aber keine Disziplinen UND keine Blöcke → nil.
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	if plan := PlanDisciplineStarts(9*3600, 0, now, nil, nil, nil); plan != nil {
		t.Fatalf("Plan = %v, want nil", plan)
	}
}
