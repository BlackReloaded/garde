package schedule

import (
	"testing"
	"time"

	"turnier/internal/model"
)

func TestPlanStartAtFutureToday(t *testing.T) {
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.Local)
	got := PlanStartAt(9*3600, now)
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("PlanStartAt = %v, want %v", got, want)
	}
}

func TestPlanStartAtPastNoRollover(t *testing.T) {
	// Heute 12:00, Start 09:00: heutige Ankunft 3 h vorbei, nächste erst
	// in 21 h (≫ 6 h) → kein Rollover, heutige (vergangene) Ankunft.
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	got := PlanStartAt(9*3600, now)
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("PlanStartAt = %v, want %v (kein Rollover)", got, want)
	}
}

func TestPlanStartAtRolloverPastMidnight(t *testing.T) {
	// Heute 23:00, Start 00:30: heutige Ankunft 22,5 h vorbei, nächste in
	// 1,5 h (≤ 6 h) → Start über Mitternacht ist gemeint → morgen 00:30.
	now := time.Date(2026, 3, 15, 23, 0, 0, 0, time.Local)
	got := PlanStartAt(30*60, now)
	want := time.Date(2026, 3, 16, 0, 30, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("PlanStartAt = %v, want %v (Rollover auf Folgetag)", got, want)
	}
}

func TestPlanStartAtRolloverWindow(t *testing.T) {
	// 6 h-Fenster: jetzt 22:00, Start 02:00 → nächste Ankunft in 4 h →
	// Rollover. Jetzt 18:00, Start 02:00 → nächste in 8 h → kein Rollover.
	now := time.Date(2026, 3, 15, 22, 0, 0, 0, time.Local)
	if got := PlanStartAt(2*3600, now); !got.Equal(time.Date(2026, 3, 16, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("22:00/02:00: PlanStartAt = %v, want 2026-03-16 02:00", got)
	}
	now = time.Date(2026, 3, 15, 18, 0, 0, 0, time.Local)
	if got := PlanStartAt(2*3600, now); !got.Equal(time.Date(2026, 3, 15, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("18:00/02:00: PlanStartAt = %v, want 2026-03-15 02:00 (kein Rollover)", got)
	}
}

func TestPlanDisciplineStartsRollover(t *testing.T) {
	// Jetzt 23:00, Turnier-Start 00:30 → Plan beginnt morgen 00:30
	// (1 Tänzer × 5-min-Slot → 00:35, keine Pause).
	now := time.Date(2026, 3, 15, 23, 0, 0, 0, time.Local)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	counts := map[int64]int{1: 1}
	plan := PlanDisciplineStarts(30*60, 0, now, discs, counts, nil)
	if len(plan) != 1 {
		t.Fatalf("Plan-Länge = %d, want 1", len(plan))
	}
	wantS := time.Date(2026, 3, 16, 0, 30, 0, 0, time.Local)
	wantE := time.Date(2026, 3, 16, 0, 35, 0, 0, time.Local)
	if !plan[0].StartEst.Equal(wantS) || !plan[0].EndEst.Equal(wantE) {
		t.Fatalf("Plan[0] = %v–%v, want %v–%v",
			plan[0].StartEst, plan[0].EndEst, wantS, wantE)
	}
}
