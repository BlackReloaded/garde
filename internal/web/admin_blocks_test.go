package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
	"turnier/internal/schedule"
)

// TestAdminBlockCRUD prüft die Block-Verwaltung per HTTP: anlegen,
// anzeigen, ändern, entfernen.
func TestAdminBlockCRUD(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	seedLive(t, st)
	ck := adminSession(t, app, "boss@x.de")
	mux := app.Handler()

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID

	// Mittagspause anlegen (Position 1, 30 min).
	form := url.Values{
		"comp_id":      {strconv.FormatInt(cid, 10)},
		"name":         {"Mittagspause"},
		"position":     {"1"},
		"duration_min": {"30"},
	}
	req := httptest.NewRequest("POST", "/admin/block/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /admin/block/create: Status %d, wollte 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Block „Mittagspause“ eingeplant") {
		t.Fatal("Flash-Nachricht fehlt")
	}
	blocks, err := st.ListPlannedBlocks(cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Name != "Mittagspause" ||
		blocks[0].Position != 1 || blocks[0].DurationSec != 30*60 {
		t.Fatalf("Blöcke = %+v, want 1× Mittagspause (Position 1, 30 min)", blocks)
	}
	// Die Admin-Seite zeigt den Block mit seinen Werten.
	body := w.Body.String()
	if !strings.Contains(body, `value="Mittagspause"`) ||
		!strings.Contains(body, `value="1" selected`) ||
		!strings.Contains(body, `value="30"`) {
		t.Fatal("Admin-Seite zeigt den Block nicht an")
	}

	// Ändern: Position am Ende, 45 min.
	bid := blocks[0].ID
	form = url.Values{
		"id":           {strconv.FormatInt(bid, 10)},
		"name":         {"Mittagspause"},
		"position":     {"-1"},
		"duration_min": {"45"},
	}
	req = httptest.NewRequest("POST", "/admin/block/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /admin/block/update: Status %d, wollte 200", w.Code)
	}
	b, err := st.GetPlannedBlock(bid)
	if err != nil {
		t.Fatal(err)
	}
	if b.Position != -1 || b.DurationSec != 45*60 {
		t.Fatalf("Block = %+v, want Position -1, 45 min", b)
	}

	// Entfernen.
	form = url.Values{"id": {strconv.FormatInt(bid, 10)}}
	req = httptest.NewRequest("POST", "/admin/block/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /admin/block/delete: Status %d, wollte 200", w.Code)
	}
	blocks, _ = st.ListPlannedBlocks(cid)
	if len(blocks) != 0 {
		t.Fatalf("Blöcke = %+v, want leer", blocks)
	}

	// Ungültige Angaben werden abgewiesen (keine Dauer).
	form = url.Values{
		"comp_id":      {strconv.FormatInt(cid, 10)},
		"name":         {"Siegerehrung"},
		"position":     {"-1"},
		"duration_min": {"0"},
	}
	req = httptest.NewRequest("POST", "/admin/block/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "Pflicht") {
		t.Fatal("Block ohne Dauer wird nicht abgewiesen")
	}
	if blocks, _ = st.ListPlannedBlocks(cid); len(blocks) != 0 {
		t.Fatalf("Abgewiesener Block existiert: %+v", blocks)
	}
}

// TestLiveShowsBlocksInPlan prüft, dass Blöcke in der Live-Ansicht im
// geplanten Ablauf auftauchen und als „Als Nächstes“ gelabelt werden.
func TestLiveShowsBlocksInPlan(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	seedLive(t, st)
	ck := adminSession(t, app, "boss@x.de")
	mux := app.Handler()

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID

	// Startzeit 3 h in der Zukunft (auf Minute): Garde (1 Tänzer =
	// 5 min), danach Mittagspause (30 min, Position 1), keine Pause
	// zwischendurch. Der komplette Plan liegt garantiert in der Zukunft.
	now := time.Now()
	start := now.Add(3 * time.Hour)
	start = time.Date(start.Year(), start.Month(), start.Day(),
		start.Hour(), start.Minute(), 0, 0, time.Local)
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	sec := int(start.Sub(midnight) / time.Second)
	if err := st.UpdateCompetitionTimes(cid, sec, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlannedBlock(model.PlannedBlock{
		CompetitionID: cid, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/?s=1&t=1&d=1", nil)
	req.AddCookie(ck)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	body := w.Body.String()

	wantGarde := "Garde " + start.Format("15:04")
	wantBlock := "Mittagspause " + start.Add(5*time.Minute).Format("15:04")
	if !strings.Contains(body, `class="plan-discs"`) || !strings.Contains(body, wantGarde) ||
		!strings.Contains(body, wantBlock) {
		t.Fatalf("Live-Ansicht: Plan-Zeile mit Blöcken fehlt (wollte %q und %q)", wantGarde, wantBlock)
	}
	// Der erste Abschnitt nach der aktuellen Disziplin ist der Block →
	// Label „Als Nächstes“ (nicht „Nächste Disziplin“).
	if !strings.Contains(body, "Als Nächstes: <b>Mittagspause</b>") {
		t.Fatal("Live-Ansicht: „Als Nächstes: Mittagspause“ fehlt")
	}
}

// TestLiveLaterDisciplineAnchorsAndNextBlock: Auch bei einer DISZIPLIN
// AM ENDE DES PLANS muss die Prognose am geplanten Start dieser
// Disziplin anknüpfen (nicht „ab jetzt“) und „als Nächstes“ den ersten
// Abschnitt NACH ihr anzeigen – nicht einen früheren Abschnitt.
func TestLiveLaterDisciplineAnchorsAndNextBlock(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	seedLive(t, st)

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID
	// Zweite Disziplin mit einem Teilnehmer.
	if _, err := st.CreateDiscipline(cid, model.Discipline{Name: "Paare"}); err != nil {
		t.Fatal(err)
	}
	discs, _ := st.ListDisciplines(cid)
	did2 := discs[1].ID
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: did2, StartNr: "1", Name: "Testpaar"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(),
		now.Hour()+3, now.Minute(), 0, 0, time.Local)
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	sec := int(start.Sub(midnight) / time.Second)
	if err := st.UpdateCompetitionTimes(cid, sec, 0); err != nil {
		t.Fatal(err)
	}
	// Mittagspause nach Disziplin 1 (30 min), Siegerehrung am Ende.
	if _, err := st.CreatePlannedBlock(model.PlannedBlock{
		CompetitionID: cid, Name: "Mittagspause", Position: 1, DurationSec: 30 * 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlannedBlock(model.PlannedBlock{
		CompetitionID: cid, Name: "Siegerehrung", Position: -1, DurationSec: 15 * 60}); err != nil {
		t.Fatal(err)
	}

	// Plan: Garde S–S+5 · Mittagspause S+5–S+35 · Paare S+35–S+40 ·
	// Siegerehrung S+40–S+55. Gesucht wird der Paare-View (d=2).
	ld, err := app.loadLive(did2, now)
	if err != nil {
		t.Fatal(err)
	}
	if ld.Sched.Current == nil || ld.Sched.Current.StartEst == nil {
		t.Fatal("Sched.Current.StartEst fehlt")
	}
	// Anker: Paare startet S+35 (Garde 5 min + Mittagspause 30 min) –
	// NICHT „ab jetzt“ und NICHT der Garde-Plan-Start.
	want := start.Add(35 * time.Minute)
	if diff := ld.Sched.Current.StartEst.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("StartEst = %v, want ≈ %v – Prognose nicht am Plan verankert",
			*ld.Sched.Current.StartEst, want)
	}
	// Als Nächstes: Siegerehrung (erster Abschnitt NACH Paare),
	// nicht Garde/Mittagspause (die liegen vor Paare).
	if ld.NextSeg == nil || ld.NextSeg.Name != "Siegerehrung" ||
		ld.NextSeg.Kind != schedule.SegmentBlock {
		t.Fatalf("NextSeg = %+v, want Siegerehrung (Block)", ld.NextSeg)
	}
}
