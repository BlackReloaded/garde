package web

import (
	"testing"
	"time"

	"turnier/internal/model"
)

// Die Startreihenfolge kommt aus der Import-/Editor-Reihenfolge
// (sort_order), nicht aus der Startnummer: Echte Startlisten führen
// Nachmeldungen ("00x") vorn in ABSTEIGender Nummer auf (002 startet vor
// 001), danach die normalen Startnummern. Die Live-Ansicht muss genau
// diese Reihenfolge zeigen – Zeitplan, "Jetzt an der Reihe" und das
// Auto-Advance des Panels nach einer vollständigen Wertung.
func TestLiveKeepsListOrderNotStartNr(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, err := st.CreateSession("2026/27")
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.CreateCompetition(sid, "Testturnier")
	if err != nil {
		t.Fatal(err)
	}
	did, err := st.CreateDiscipline(cid, model.Discipline{Name: "Solo"})
	if err != nil {
		t.Fatal(err)
	}
	// Bewusst NICHT numerisch: erst die Nachmeldungen 002/001, dann 1/2.
	for _, p := range []model.Participant{
		{StartNr: "002", Name: "Zweite Nachmeldung"},
		{StartNr: "001", Name: "Erste Nachmeldung"},
		{StartNr: "1", Name: "Feld eins"},
		{StartNr: "2", Name: "Feld zwei"},
	} {
		if _, err := st.CreateParticipant(model.Participant{
			Discipline: did, StartNr: p.StartNr, Name: p.Name}); err != nil {
			t.Fatal(err)
		}
	}
	ld, err := app.loadLive(did, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"002", "001", "1", "2"}
	if len(ld.SchedRows) != len(want) {
		t.Fatalf("SchedRows: n=%d, wollte %d", len(ld.SchedRows), len(want))
	}
	for i, r := range ld.SchedRows {
		if r.StartNr != want[i] {
			t.Fatalf("SchedRows-Reihenfolge = %v, want %v (Startreihenfolge = Listenreihenfolge)",
				ld.SchedRows, want)
		}
	}
	if ld.Sched.Current == nil || ld.Sched.Current.StartNr != "002" {
		t.Fatalf("Current = %+v, want 002", ld.Sched.Current)
	}
	if ld.Sched.Next == nil || ld.Sched.Next.StartNr != "001" {
		t.Fatalf("Next = %+v, want 001", ld.Sched.Next)
	}

	// 002 komplett bewerten → das Panel springt auf den Nächsten in der
	// Liste (001), nicht auf die kleinste Nummer.
	second, err := st.ListParticipantsWithScores(did)
	if err != nil {
		t.Fatal(err)
	}
	var firstID int64
	for _, p := range second {
		if p.StartNr == "002" {
			firstID = p.ID
		}
	}
	if firstID == 0 {
		t.Fatal("Teilnehmer 002 nicht gefunden")
	}
	for j := 1; j <= ld.NumJ; j++ {
		if err := st.SetScore(firstID, j, 50); err != nil {
			t.Fatal(err)
		}
	}
	ld2, err := app.loadLive(did, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ld2.Sched.Current == nil || ld2.Sched.Current.StartNr != "001" {
		t.Fatalf("Current nach 7/7 = %+v, want 001", ld2.Sched.Current)
	}
}
