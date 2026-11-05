package schedule

import (
	"testing"
	"time"

	"tuniert/internal/model"
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
	set := Settings{MaxDanceSec: 180, ScoringDefSec: 720, WalkOnSec: 120, NumJudges: 7}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "A"},
		{ID: 2, StartNr: "2", Name: "B"},
		{ID: 3, StartNr: "3", Name: "C"},
	}
	res := Compute(set, ps, now)
	// Default-Slot = 120 + 180 + 720 = 1020 s
	if res.SlotSec != 1020 {
		t.Fatalf("SlotSec = %d, want 1020", res.SlotSec)
	}
	if !res.Current.IsCurrent || res.Current.ID != 1 {
		t.Fatal("Current sollte Teilnehmer 1 sein")
	}
	// Teilnehmer 1: Start = now, Ende = now + 1020 s
	if res.Entries[0].StartEst == nil || !res.Entries[0].StartEst.Equal(now) {
		t.Fatalf("StartEst[0] = %v, want %v", res.Entries[0].StartEst, now)
	}
	wantEnd1 := now.Add(1020 * time.Second)
	if !res.Entries[0].EndEst.Equal(wantEnd1) {
		t.Fatalf("EndEst[0] = %v, want %v", res.Entries[0].EndEst, wantEnd1)
	}
	// Teilnehmer 2: Start = now + 1020, Ende = now + 2040
	wantStart2 := now.Add(1020 * time.Second)
	if !res.Entries[1].StartEst.Equal(wantStart2) {
		t.Fatalf("StartEst[1] = %v, want %v", res.Entries[1].StartEst, wantStart2)
	}
	// Block-Ende = now + 3 * 1020
	wantBlock := now.Add(3 * 1020 * time.Second)
	if !res.BlockEnd.Equal(wantBlock) {
		t.Fatalf("BlockEnd = %v, want %v", res.BlockEnd, wantBlock)
	}
}

func TestComputeWithObservations(t *testing.T) {
	set := Settings{MaxDanceSec: 180, ScoringDefSec: 720, WalkOnSec: 120, NumJudges: 7}
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)

	// P1: 7 Noten zwischen 13:10:00 und 13:20:00 (Punktezeit 10 min)
	// P2: 7 Noten zwischen 13:25:00 und 13:35:00 (Punktezeit 10 min)
	//    -> Slot = 13:35 - 13:20 = 15 min = 900 s
	// P3: 3 Noten seit 13:50:00 -> wird gerade bewertet
	// P4: keine Noten
	p1 := mkScores(7, now.Add(-50*time.Minute), 0, 100*time.Second)
	p2 := mkScores(7, now.Add(-35*time.Minute), 0, 100*time.Second)
	p3 := mkScores(3, now.Add(-10*time.Minute), 0, 90*time.Second)
	ps := []model.ParticipantInfo{
		{ID: 1, StartNr: "1", Name: "P1", Scores: p1},
		{ID: 2, StartNr: "2", Name: "P2", Scores: p2},
		{ID: 3, StartNr: "3", Name: "P3", Scores: p3},
		{ID: 4, StartNr: "4", Name: "P4"},
	}
	res := Compute(set, ps, now)
	// Slot: letzte Note P2 = 13:35, letzte P1 = 13:20 -> 15 min = 900 s
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
	set := Settings{MaxDanceSec: 180, ScoringDefSec: 720, WalkOnSec: 120, NumJudges: 7}
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
