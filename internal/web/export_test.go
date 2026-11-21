package web

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"turnier/internal/model"
	"turnier/internal/store"
)

// seedProtocolDisc legt eine minimale Hierarchie an (Session → Turnier →
// Disziplin "Garde" mit 7 Richtern) – ohne Teilnehmer.
func seedProtocolDisc(t *testing.T, st *store.Store) (sid, cid, did int64) {
	t.Helper()
	var err error
	sid, err = st.CreateSession("Heft-Saison")
	if err != nil {
		t.Fatal(err)
	}
	cid, err = st.CreateCompetition(sid, "Heft-Turnier")
	if err != nil {
		t.Fatal(err)
	}
	did, err = st.CreateDiscipline(cid, model.Discipline{Name: "Garde", NumJudges: 7})
	if err != nil {
		t.Fatal(err)
	}
	return sid, cid, did
}

// addDancer legt einen Tänzer an; scores sind die Noten der Richter 1..n.
func addDancer(t *testing.T, st *store.Store, did int64, nr, name string, scores []int) int64 {
	t.Helper()
	pid, err := st.CreateParticipant(model.Participant{Discipline: did, StartNr: nr, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	for j, v := range scores {
		if err := st.SetScore(pid, j+1, v); err != nil {
			t.Fatal(err)
		}
	}
	return pid
}

// Das Protokoll enthält die komplett bewerteten Tänze in Startreihenfolge
// mit Einzelnoten – auch für Gäste ohne Login (reines Lese-Recht).
func TestExportProtocolPDF(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st)
	addDancer(t, st, did, "1", "Erste Garde", []int{100, 90, 80, 70, 60, 50, 45})
	addDancer(t, st, did, "2", "Zweite Garde", []int{90, 90, 90}) // unvollständig
	mux := app.Handler()

	url := fmt.Sprintf("/export/protocol?s=%d&t=%d&d=%d", sid, cid, did)
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("Status %d, Body: %.200q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "%PDF-") {
		t.Fatal("Body beginnt nicht mit %PDF-")
	}
	if !strings.Contains(body, "Ergebnisprotokoll") {
		t.Fatal("Titel fehlt im PDF")
	}
	if !strings.Contains(body, "Erste Garde") {
		t.Fatal("fertiger Tänzer fehlt im Protokoll")
	}
	if strings.Contains(body, "Zweite Garde") {
		t.Fatal("unvollständiger Tänzer darf nicht im Protokoll stehen")
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `ergebnisprotokoll-garde.pdf`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

// Ohne komplette Wertung entsteht weiter ein PDF – mit Hinweistext statt
// Tabelle (der Knopf soll nie ins Leere führen).
func TestExportProtocolEmpty(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st)
	addDancer(t, st, did, "1", "Erste Garde", []int{80, 80}) // unfertig
	mux := app.Handler()

	url := fmt.Sprintf("/export/protocol?s=%d&t=%d&d=%d", sid, cid, did)
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("Status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "keine komplette Wertung") {
		t.Fatal("Hinweistext fehlt")
	}
}

// Unbekannte Disziplinen-ID: 404 statt Fehlerseite.
func TestExportProtocolUnknownDisc(t *testing.T) {
	app, _ := newTestApp(t, AuthConfig{})
	mux := app.Handler()
	req := httptest.NewRequest("GET", "/export/protocol?d=999", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("Status %d, want 404", w.Code)
	}
}

func TestTo1252(t *testing.T) {
	// Umlaute sind Latin-1 (identische bytes), der Stern wird ASCII-Star,
	// der Gedankenstrich cp1252-0x96.
	got := to1252("Mädchen ★ –")
	want := "M" + "\xe4" + "dchen " + "*" + " " + "\x96"
	if got != want {
		t.Fatalf("to1252 = %q, want %q", got, want)
	}
}

func TestPDFFieldName(t *testing.T) {
	// Dateiname muss ASCII und ohne Sonderzeichen sein.
	if got := pdfFileName("Jugend Garde"); got != "ergebnisprotokoll-jugend-garde.pdf" {
		t.Fatalf("pdfFileName = %q", got)
	}
	if got := pdfFileName("Ü15 Solo / Gemischt"); got != "ergebnisprotokoll-ue15-solo-gemischt.pdf" {
		t.Fatalf("pdfFileName = %q", got)
	}
	if got := pdfFileName(""); got != "ergebnisprotokoll-export.pdf" {
		t.Fatalf("pdfFileName = %q", got)
	}
}
