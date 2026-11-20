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
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
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
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)

	// P1: 7 Noten zwischen 13:10:00 und 13:20:00 (Punktezeit 10 min)
	// P2: 7 Noten zwischen 13:25:00 und 13:35:00 (Punktezeit 10 min)
	//    -> Gap 1 = 13:25 - 13:20 = 5 min, Gap 2 = 13:40 - 13:35 = 5 min
	// P3: 3 Noten seit 13:40:00 -> wird gerade bewertet
	// P4: keine Noten
	// => Slot = Ø Gap (5 min) + Ø Punktezeit (10 min) = 900 s
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
	// Punktezeit: P1 10 min, P2 10 min -> 600 s
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
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
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

func TestComputeGapStartsEarly(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
	// P1 fertig: 7 Noten von 13:10 bis 13:20 (Punktezeit 600 s).
	// P2: erst 2 Noten, erste um 13:25 -> Gap = 5 min = 300 s.
	// Der Slot ist also schon "von Bewertung zur nächsten Bewertung"
	// messbar, ohne auf P2s 7. Note zu warten (alt: Default 300 s).
	// P3: noch keine Noten.
	p1 := mkScores(7, now.Add(-50*time.Minute), 0, 100*time.Second)
	p2 := mkScores(2, now.Add(-35*time.Minute), 0, 90*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
		{ID: 3, StartNr: "3", Name: "P3"},
	}
	res := Compute(set, ps, now)
	if res.ScoringSec != 600 {
		t.Fatalf("ScoringSec = %d, want 600", res.ScoringSec)
	}
	// Slot = Gap 300 + Punktezeit 600 = 900 s (nicht der Default 1020 s).
	if res.SlotSec != 900 {
		t.Fatalf("SlotSec = %d, want 900", res.SlotSec)
	}
	// Current = P2 (2/7): Rest = 600*5/7 ≈ 428 s
	wantEnd2 := now.Add(428 * time.Second)
	if got := res.Entries[1].EndEst; got.Sub(wantEnd2) > time.Second {
		t.Fatalf("EndEst[1] = %v, want ≈ %v", got, wantEnd2)
	}
	// P3: Ende = Ende P2 + Slot
	wantEnd3 := wantEnd2.Add(900 * time.Second)
	if got := res.Entries[2].EndEst; got.Sub(wantEnd3) > time.Second {
		t.Fatalf("EndEst[2] = %v, want ≈ %v", got, wantEnd3)
	}
}

func TestComputeBreakIgnored(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
	// P1 fertig: letzte Note um 13:06.
	// P2: erste Note erst um 14:00 (54 min später = Pause) ->
	// das Gap wird verworfen, es greift der Default (300 s).
	p1 := mkScores(7, now.Add(-60*time.Minute), 0, 60*time.Second)
	p2 := mkScores(1, now, 0, 0)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
	}
	res := Compute(set, ps, now)
	// Punktezeit P1 = 6*60 = 360 s; Gap = Default 60+120 = 180 s.
	if res.ScoringSec != 360 {
		t.Fatalf("ScoringSec = %d, want 360", res.ScoringSec)
	}
	if res.SlotSec != 540 {
		t.Fatalf("SlotSec = %d, want 540 (Default-Gap 180 + Punktezeit 360)", res.SlotSec)
	}
}

func TestComputeAnchorAtPlannedStart(t *testing.T) {
	set := Settings{MaxDanceSec: model.DefaultMaxDanceSec, ScoringDefSec: model.DefaultScoringSec,
		WalkOnSec: model.DefaultWalkOnSec, NumJudges: model.DefaultNumJudges}
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.Local)
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
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
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
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
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

func TestPlanDisciplineStarts(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 2, Name: "Leer", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 3, Name: "Paare", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	counts := map[int64]int{1: 2, 3: 3} // Disziplin 2 leer
	// Turnier-Start 09:00, keine Pause, Slot-Default je Disziplin 300 s.
	plan := PlanDisciplineStarts(9*3600, now, discs, counts, nil)
	if len(plan) != 2 {
		t.Fatalf("Plan-Länge = %d, want 2 (leere Disziplin übersprungen)", len(plan))
	}
	want1s := time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local)
	want1e := time.Date(2026, 3, 15, 9, 10, 0, 0, time.Local)
	// 2 Tänzer × 5 min = 10 min.
	if !plan[0].StartEst.Equal(want1s) || !plan[0].EndEst.Equal(want1e) {
		t.Fatalf("Plan[0] = %v–%v, want %v–%v",
			plan[0].StartEst, plan[0].EndEst, want1s, want1e)
	}
	// direkt danach (ohne Pause): Paare 09:10; 3 Tänzer × 5 min = 15 min -> 09:25.
	want3s := time.Date(2026, 3, 15, 9, 10, 0, 0, time.Local)
	want3e := time.Date(2026, 3, 15, 9, 25, 0, 0, time.Local)
	if !plan[1].StartEst.Equal(want3s) || !plan[1].EndEst.Equal(want3e) {
		t.Fatalf("Plan[1] = %v–%v, want %v–%v",
			plan[1].StartEst, plan[1].EndEst, want3s, want3e)
	}
	if plan[1].ID != 3 {
		t.Fatalf("Plan[1].ID = %d, want 3", plan[1].ID)
	}
}

func TestPlanDisciplineNoFixedGaps(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
		{ID: 2, Name: "Paare", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	counts := map[int64]int{1: 1, 2: 1}
	plan := PlanDisciplineStarts(9*3600, now, discs, counts, nil)
	// Garde 09:00–09:05, direkt danach Paare 09:05–09:10 – ohne feste
	// Pause zwischen den Disziplinen.
	wantEnd := time.Date(2026, 3, 15, 9, 10, 0, 0, time.Local)
	if !plan[1].EndEst.Equal(wantEnd) {
		t.Fatalf("Plan[1].EndEst = %v, want %v", plan[1].EndEst, wantEnd)
	}
}

func TestPlanDisciplineNoStart(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	discs := []model.Discipline{
		{ID: 1, Name: "Garde", MaxDanceSec: 120, ScoringDefSec: 120, WalkOnSec: 60, NumJudges: 7},
	}
	counts := map[int64]int{1: 1}
	if plan := PlanDisciplineStarts(0, now, discs, counts, nil); plan != nil {
		t.Fatalf("Plan = %v, want nil (keine Startzeit gesetzt)", plan)
	}
}
