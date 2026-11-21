package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
)

// Der Zeitplan zeigt nur die noch ausstehenden Tänze. Was schon Noten hat,
// ist in der "In Bewertung"-Karte (1–6 Noten) bzw. in der Rangliste (7/7)
// sichtbar und muss in der Zeitplan-Tabelle nicht weiter mitlaufen – sonst
// wächst die Liste im laufenden Betrieb auf die komplette Startliste an.
// Zurückgezogene fallen weg, sobald die Liste über sie hinweg ist.
func TestScheduleShowsOnlyUpcoming(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	mux := app.Handler()

	sid, err := st.CreateSession("2026/27")
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.CreateCompetition(sid, "Testturnier")
	if err != nil {
		t.Fatal(err)
	}
	did, err := st.CreateDiscipline(cid, model.Discipline{Name: "Garde"})
	if err != nil {
		t.Fatal(err)
	}
	// Liste: 1 (läuft gleich durch), 2 (mitten in der Bewertung),
	// 3 (wartet), 4 (zurückgezogen, HINTER dem laufenden Tänzer).
	one, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "1", Name: "Längst fertig"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "2", Name: "Wird bewertet"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "3", Name: "Wartet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "4", Name: "Gestrichen", Withdrawn: true}); err != nil {
		t.Fatal(err)
	}

	numJ := model.DefaultNumJudges
	for j := 1; j <= numJ; j++ {
		if err := st.SetScore(one, j, 50); err != nil {
			t.Fatal(err)
		}
	}
	for j := 1; j <= 3; j++ {
		if err := st.SetScore(two, j, 50); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	ld, err := app.loadLive(did, now)
	if err != nil {
		t.Fatal(err)
	}
	if ld.Sched.Current == nil || ld.Sched.Current.StartNr != "2" {
		t.Fatalf("Current = %+v, want 2", ld.Sched.Current)
	}
	want := []string{"3", "4"}
	if len(ld.SchedRows) != len(want) {
		t.Fatalf("SchedRows = %+v, wollte nur %v", ld.SchedRows, want)
	}
	for i, nr := range want {
		if ld.SchedRows[i].StartNr != nr {
			t.Fatalf("SchedRows = %+v, wollte %v", ld.SchedRows, want)
		}
	}
	if !ld.SchedRows[1].Withdrawn {
		t.Fatal("gestrichener Tänzer hinter dem laufenden müsste weiter zeigen")
	}
	// "In Bewertung" = nur die echte Teildwertung; der wartende Tänzer
	// gehört in den Zeitplan, nicht in die Bewertungskarte.
	if len(ld.Pending) != 1 || ld.Pending[0].P.StartNr != "2" {
		t.Fatalf("Pending = %+v, want nur 2", ld.Pending)
	}

	// Alles gelaufen: Tabelle leer, Seite rendert trotzdem (Leerfall-Text).
	three := int64(0)
	parts, err := st.ListParticipants(did)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if p.StartNr == "3" {
			three = p.ID
		}
	}
	if three == 0 {
		t.Fatal("Teilnehmer 3 nicht gefunden")
	}
	for j := 1; j <= numJ; j++ {
		if err := st.SetScore(two, j, 60); err != nil {
			t.Fatal(err)
		}
		if err := st.SetScore(three, j, 60); err != nil {
			t.Fatal(err)
		}
	}
	ld2, err := app.loadLive(did, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !ld2.Finished {
		t.Fatalf("Fertig-Status nicht gesetzt: %+v", ld2.Sched.Finished)
	}
	if len(ld2.SchedRows) != 0 {
		t.Fatalf("Nach Abschluss müssen alle Zeitplan-Zeilen wegfallen: %+v", ld2.SchedRows)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Keine Tänze mehr voraus") {
		t.Fatal("Leerfall-Hinweis des Zeitplans fehlt")
	}
}
