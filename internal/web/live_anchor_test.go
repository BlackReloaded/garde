package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
)

// Die Zeitprognose muss an die geplante Turnier-Startzeit anknüpfen und
// nicht "ab jetzt" rechnen, solange noch keine Note existiert. Der Start
// wird 2 h in der Zukunft gesetzt (überquert abends Mitternacht – deckt
// damit auch den Tages-Rollover von schedule.PlanStartAt ab).
func TestLiveAnchorsToPlannedStart(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	seedLive(t, st)

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID
	discs, _ := st.ListDisciplines(cid)
	did := discs[0].ID

	// Zweiten Teilnehmer ergänzen (beide ohne Noten).
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "2", Name: "Zweite Garde"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	start := now.Add(2 * time.Hour)
	sec := int(start.Hour())*3600 + int(start.Minute())*60 + int(start.Second())
	if err := st.UpdateCompetitionStart(cid, sec); err != nil {
		t.Fatal(err)
	}

	ld, err := app.loadLive(did, now)
	if err != nil {
		t.Fatal(err)
	}
	if ld.Sched.Current == nil || ld.Sched.Current.StartEst == nil {
		t.Fatal("Sched.Current.StartEst fehlt")
	}
	got := *ld.Sched.Current.StartEst
	if diff := got.Sub(start); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("StartEst = %v, erwartet ≈ %v (Differenz %v) – Prognose nicht am Plan verankert",
			got, start, diff)
	}
	// Block-Ende liegt ebenfalls im Plan (2 Teilnehmer × Default-Slot 300 s).
	if ld.Sched.BlockEnd == nil {
		t.Fatal("Sched.BlockEnd fehlt")
	}
	wantEnd := start.Add(2 * 300 * time.Second)
	if diff := ld.Sched.BlockEnd.Sub(wantEnd); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("BlockEnd = %v, want ≈ %v", *ld.Sched.BlockEnd, wantEnd)
	}
}

// Die Live-Ansicht zeigt bei anstehendem (geplantem) Start die Zeit an
// statt "start ≈ jetzt" – der Zeitplan zeigt damit den Turnier-Plan an.
func TestLiveScheduleShowsPlannedStart(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	seedLive(t, st)

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID
	discs, _ := st.ListDisciplines(cid)
	did := discs[0].ID

	now := time.Now()
	start := now.Add(90 * time.Minute)
	sec := int(start.Hour())*3600 + int(start.Minute())*60 + int(start.Second())
	if err := st.UpdateCompetitionStart(cid, sec); err != nil {
		t.Fatal(err)
	}

	ld, err := app.loadLive(did, now)
	if err != nil {
		t.Fatal(err)
	}
	buf := bytes.Buffer{}
	if err := app.tmpl["live"].ExecuteTemplate(&buf, "schedule", ld); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if want := "start ca. " + start.Format("15:04"); !strings.Contains(body, want) {
		t.Fatalf("Zeitplan zeigt %q nicht", want)
	}
	if strings.Contains(body, "start ≈ jetzt") {
		t.Fatal("Zeitplan zeigt 'start ≈ jetzt', obwohl ein Plan-Start in der Zukunft liegt")
	}
}
