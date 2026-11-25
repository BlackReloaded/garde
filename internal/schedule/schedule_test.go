package schedule

import (
	"testing"
	"time"

	"turnier/internal/model"
)

func mkScores(n int, start time.Time, step time.Duration, spread time.Duration) []model.ScoreAt {
	out := make([]model.ScoreAt, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.ScoreAt{Value: 80, At: start.Add(time.Duration(i) * spread)})
	}
	_ = step
	return out
}

func TestComputeNoObservations(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "A"},
		{ID: 2, StartNr: "2", Name: "B"},
		{ID: 3, StartNr: "3", Name: "C"},
	}
	res := Compute(set, ps, now)
	// Default-Slot = 60 + 120 + 120 = 300 s (5-Minuten-Takt)
	if res.SlotSec != 300 {
		t.Fatalf("SlotSec = %d, want 300", res.SlotSec)
	}
	if !res.Current.IsCurrent || res.Current.ID != 1 {
		t.Fatal("Current sollte Teilnehmer 1 sein")
	}
	// Teilnehmer 1: Start = now, Ende = now + 300 s
	if res.Entries[0].StartEst == nil || !res.Entries[0].StartEst.Equal(now) {
		t.Fatalf("StartEst[0] = %v, want %v", res.Entries[0].StartEst, now)
	}
	wantEnd1 := now.Add(300 * time.Second)
	if !res.Entries[0].EndEst.Equal(wantEnd1) {
		t.Fatalf("EndEst[0] = %v, want %v", res.Entries[0].EndEst, wantEnd1)
	}
	// Teilnehmer 2: Start = now + 300, Ende = now + 600
	wantStart2 := now.Add(300 * time.Second)
	if !res.Entries[1].StartEst.Equal(wantStart2) {
		t.Fatalf("StartEst[1] = %v, want %v", res.Entries[1].StartEst, wantStart2)
	}
	// Block-Ende = now + 3 * 300
	wantBlock := now.Add(3 * 300 * time.Second)
	if !res.BlockEnd.Equal(wantBlock) {
		t.Fatalf("BlockEnd = %v, want %v", res.BlockEnd, wantBlock)
	}
}

func TestComputeWithObservations(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)

	// P1: 7 Noten zwischen 13:10:00 und 13:20:00 – erster Tanz, fließt
	//     nicht in die Messung ein.
	// P2: 7 Noten zwischen 13:25:00 und 13:35:00 (Punktezeit 10 min)
	//     -> als zweiter Tanz gemessen.
	// P3: 3 Noten seit 13:40:00 -> wird gerade bewertet; der Übergang
	//     hinter P2 (13:35 → 13:40 = 5 min) ist schon messbar.
	// => Slot = Ø Gap (5 min) + Ø Punktezeit (10 min, nur P2) = 900 s
	p1 := mkScores(7, now.Add(-50*time.Minute), 0, 100*time.Second)
	p2 := mkScores(7, now.Add(-35*time.Minute), 0, 100*time.Second)
	p3 := mkScores(3, now.Add(-20*time.Minute), 0, 90*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
		{ID: 3, StartNr: "3", Name: "P3", Scores: p3},
		{ID: 4, StartNr: "4", Name: "P4"},
	}
	res := Compute(set, ps, now)
	// Slot: Ø Gap (300 s) + Ø Punktezeit (600 s) = 900 s
	if res.SlotSec != 900 {
		t.Fatalf("SlotSec = %d, want 900", res.SlotSec)
	}
	// Punktezeit: nur P2 (10 min) – P1 als erster Tanz zählt nicht mit.
	if res.ScoringSec != 600 {
		t.Fatalf("ScoringSec = %d, want 600", res.ScoringSec)
	}
	if res.Current == nil || res.Current.ID != 3 {
		t.Fatal("Current sollte P3 sein")
	}
	// P3: 3/7 Noten, Rest = 600 * 4/7 ≈ 342 s -> Ende ≈ now + 342 s
	wantEnd3 := now.Add(342 * time.Second)
	if got := res.Entries[2].EndEst; got.Sub(wantEnd3) > time.Second {
		t.Fatalf("EndEst[2] = %v, want ≈ %v", got, wantEnd3)
	}
	// P4: Start = Ende des P3-Slots = now + 342 s; Ende = Start + Slot = now + 1242 s
	wantStart4 := now.Add(342 * time.Second)
	if got := res.Entries[3].StartEst; got.Sub(wantStart4) > time.Second {
		t.Fatalf("StartEst[3] = %v, want ≈ %v", got, wantStart4)
	}
	wantEnd4 := now.Add(1242 * time.Second)
	if got := res.Entries[3].EndEst; got.Sub(wantEnd4) > time.Second {
		t.Fatalf("EndEst[3] = %v, want ≈ %v", got, wantEnd4)
	}
}

func TestComputeFinished(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	p1 := mkScores(7, now.Add(-20*time.Minute), 0, 60*time.Second)
	p2 := mkScores(7, now.Add(-8*time.Minute), 0, 60*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
	}
	res := Compute(set, ps, now)
	if !res.Finished {
		t.Fatal("Expected Finished=true")
	}
	if res.Current != nil {
		t.Fatal("Current darf nicht gesetzt sein")
	}
	lastP2 := p2[len(p2)-1].At
	if !res.BlockEnd.Equal(lastP2) {
		t.Fatalf("BlockEnd = %v, want %v", res.BlockEnd, lastP2)
	}
}

func TestComputeFirstDanceExcluded(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	// P1 (erster Tanz): 7 Noten 13:10–13:20 (10 min). Der aufgeblähte
	// Start würde den Schnitt verzerren (auf 9 min) – er bleibt daher
	// außen vor.
	// P2: 7 Noten 13:25–13:33 (8 min) -> Punktezeit nur hieraus: 480 s.
	// P3: 3 Noten seit 13:40 -> Übergang hinter P2 = 13:33 → 13:40 =
	//     420 s, schon mit der ersten Note messbar (nicht erst nach der
	//     7.). Der Übergang hinter P1 (13:20 → 13:25) wird nicht gemessen.
	// => Slot = 420 + 480 = 900 s.
	p1 := mkScores(7, now.Add(-50*time.Minute), 0, 100*time.Second)
	p2 := mkScores(7, now.Add(-35*time.Minute), 0, 80*time.Second)
	p3 := mkScores(3, now.Add(-20*time.Minute), 0, 90*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
		{ID: 3, StartNr: "3", Name: "P3", Scores: p3},
	}
	res := Compute(set, ps, now)
	if res.ScoringSec != 480 {
		t.Fatalf("ScoringSec = %d, want 480 (nur P2, P1 als Starttanz nicht gemessen)", res.ScoringSec)
	}
	if res.SlotSec != 900 {
		t.Fatalf("SlotSec = %d, want 900 (420 + 480)", res.SlotSec)
	}
	if res.Current == nil || res.Current.ID != 3 {
		t.Fatal("Current sollte P3 sein")
	}
	// P3: 3/7 Noten, Rest = 480 * 4/7 ≈ 274 s -> Ende ≈ now + 274 s
	wantEnd3 := now.Add(274 * time.Second)
	if got := res.Entries[2].EndEst; got.Sub(wantEnd3) > time.Second {
		t.Fatalf("EndEst[2] = %v, want ≈ %v", got, wantEnd3)
	}
}

func TestComputeBreakIgnored(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	// P1 (erster Tanz, 12:40–12:46) liefert ohnehin keine Messwerte.
	// P2 komplett 12:50–13:00 -> Punktezeit 600 s.
	// P3: erste Note erst um 14:00 (60 min nach P2 = Pause) -> das Gap
	// wird verworfen, es greift der Default (180 s).
	p1 := mkScores(7, now.Add(-80*time.Minute), 0, 60*time.Second)
	p2 := mkScores(7, now.Add(-70*time.Minute), 0, 100*time.Second)
	p3 := mkScores(1, now, 0, 0)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
		{ID: 3, StartNr: "3", Name: "P3", Scores: p3},
	}
	res := Compute(set, ps, now)
	// Punktezeit nur aus P2 (P1 bleibt außen vor); Gap = Default.
	if res.ScoringSec != 600 {
		t.Fatalf("ScoringSec = %d, want 600", res.ScoringSec)
	}
	if res.SlotSec != 780 {
		t.Fatalf("SlotSec = %d, want 780 (Default-Gap 180 + Punktezeit 600)", res.SlotSec)
	}
}

func TestComputeAnchorAtPlannedStart(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, Zone)
	anchor := now.Add(1 * time.Hour)
	set.AnchorAt = &anchor
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "A"},
		{ID: 2, StartNr: "2", Name: "B"},
	}
	res := Compute(set, ps, now)
	// res.Now bleibt "jetzt", der Plan verschiebt nur die Schätzungen.
	if !res.Now.Equal(now) {
		t.Fatalf("Now = %v, want %v", res.Now, now)
	}
	if res.Current == nil || res.Current.StartEst == nil {
		t.Fatal("Current.StartEst fehlt")
	}
	// Teilnehmer 1 startet pünktlich zum geplanten Start (nicht "ab jetzt").
	if !res.Current.StartEst.Equal(anchor) {
		t.Fatalf("StartEst[0] = %v, want %v", res.Current.StartEst, anchor)
	}
	// Ende = Anker + 1 Slot (Default 300 s).
	if !res.Current.EndEst.Equal(anchor.Add(300 * time.Second)) {
		t.Fatalf("EndEst[0] = %v, want %v", res.Current.EndEst, anchor.Add(300*time.Second))
	}
	// Teilnehmer 2: Anker + 1 Slot.
	if res.Next == nil || res.Next.StartEst == nil ||
		!res.Next.StartEst.Equal(anchor.Add(300*time.Second)) {
		t.Fatalf("StartEst[1] = %v, want %v",
			res.Next.StartEst, anchor.Add(300*time.Second))
	}
	// Block-Ende = Anker + 2 Slots.
	if !res.BlockEnd.Equal(anchor.Add(2 * 300 * time.Second)) {
		t.Fatalf("BlockEnd = %v, want %v", res.BlockEnd, anchor.Add(600*time.Second))
	}
}

func TestComputeAnchorPastIgnored(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	past := now.Add(-2 * time.Hour)
	set.AnchorAt = &past
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "A"},
	}
	res := Compute(set, ps, now)
	// Geplanter Start in der Vergangenheit (Turnier ist spät gestartet)
	// -> Prognose startet "ab jetzt", wie ohne Plan.
	if res.Current == nil || res.Current.StartEst == nil || !res.Current.StartEst.Equal(now) {
		t.Fatalf("StartEst = %v, want %v", res.Current.StartEst, now)
	}
}

func TestComputeAnchorWithScoresIgnored(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, Zone)
	future := now.Add(3 * time.Hour)
	set.AnchorAt = &future
	// P1 fertig (7 Noten), P2: 2 Noten seit kurz -> es existieren Noten,
	// der Plan-Anchor darf nicht greifen.
	p1 := mkScores(7, now.Add(-20*time.Minute), 0, 60*time.Second)
	p2 := mkScores(2, now.Add(-3*time.Minute), 0, 90*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
	}
	res := Compute(set, ps, now)
	if res.Current == nil || res.Current.ID != 2 {
		t.Fatal("Current sollte P2 sein")
	}
	// P2 wird bewertet (2/7): Ende = now + Punktezeit * 5/7 – NICHT der
	// Plan-Anchor in 3 h.
	if !res.Current.EndEst.Before(future) {
		t.Fatalf("EndEst = %v, liegt nach dem Plan-Anchor %v – Anchor greift fälschlich",
			res.Current.EndEst, future)
	}
}

func TestPlanTimeline(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, Zone)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 2, Name: "Leer", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 3, Name: "Paare", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	acts := map[int64]DiscActual{1: {Total: 2}, 3: {Total: 3}} // Disziplin 2 leer
	// Turnier-Start 09:00, keine Pause, Slot-Default je Disziplin 300 s.
	plan := PlanTimeline(9*3600, now, Zone, discs, nil, acts)
	if len(plan) != 2 {
		t.Fatalf("Plan-Länge = %d, want 2 (leere Disziplin übersprungen)", len(plan))
	}
	want1s := time.Date(2026, 3, 15, 9, 0, 0, 0, Zone)
	want1e := time.Date(2026, 3, 15, 9, 10, 0, 0, Zone)
	// 2 Tänzer × 5 min = 10 min.
	if !plan[0].StartEst.Equal(want1s) || !plan[0].EndEst.Equal(want1e) {
		t.Fatalf("Plan[0] = %v–%v, want %v–%v",
			plan[0].StartEst, plan[0].EndEst, want1s, want1e)
	}
	// direkt danach (ohne Pause): Paare 09:10; 3 Tänzer × 5 min = 15 min -> 09:25.
	want3s := time.Date(2026, 3, 15, 9, 10, 0, 0, Zone)
	want3e := time.Date(2026, 3, 15, 9, 25, 0, 0, Zone)
	if !plan[1].StartEst.Equal(want3s) || !plan[1].EndEst.Equal(want3e) {
		t.Fatalf("Plan[1] = %v–%v, want %v–%v",
			plan[1].StartEst, plan[1].EndEst, want3s, want3e)
	}
	if plan[1].ID != 3 {
		t.Fatalf("Plan[1].ID = %d, want 3", plan[1].ID)
	}
}

func TestPlanDisciplineNoFixedGaps(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, Zone)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 2, Name: "Paare", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	acts := map[int64]DiscActual{1: {Total: 1}, 2: {Total: 1}}
	plan := PlanTimeline(9*3600, now, Zone, discs, nil, acts)
	// Garde 09:00–09:05, direkt danach Paare 09:05–09:10 – ohne feste
	// Pause zwischen den Disziplinen.
	wantEnd := time.Date(2026, 3, 15, 9, 10, 0, 0, Zone)
	if !plan[1].EndEst.Equal(wantEnd) {
		t.Fatalf("Plan[1].EndEst = %v, want %v", plan[1].EndEst, wantEnd)
	}
}

func TestPlanDisciplineNoStart(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, Zone)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	acts := map[int64]DiscActual{1: {Total: 1}}
	if plan := PlanTimeline(0, now, Zone, discs, nil, acts); plan != nil {
		t.Fatalf("Plan = %v, want nil (keine Startzeit gesetzt)", plan)
	}
}
