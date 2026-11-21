package web

import (
	"compress/zlib"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"turnier/internal/model"
	"turnier/internal/store"
)

// seedProtocolDisc legt eine minimale Hierarchie an (Session → Turnier
// "Heft-Turnier" → Disziplin) – ohne Teilnehmer.
func seedProtocolDisc(t *testing.T, st *store.Store, numJ int) (sid, cid, did int64) {
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
	did, err = st.CreateDiscipline(cid, model.Discipline{Name: "Garde", NumJudges: numJ})
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

func getURL(t *testing.T, app *App, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	return w
}

// embeddedCSV holt den eingebetteten CSV-Anhang zurück (entpackt) bzw. ""
// wenn kein Anhang drin ist.
func embeddedCSV(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "/Type /EmbeddedFile")
	if i < 0 {
		return ""
	}
	s := strings.Index(body[i:], "stream\n")
	if s < 0 {
		t.Fatal("EmbeddedFile ohne stream")
	}
	r, err := zlib.NewReader(strings.NewReader(body[i+s+len("stream\n"):]))
	if err != nil {
		t.Fatalf("EmbeddedFile nicht lesbar: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("EmbeddedFile entpacken: %v", err)
	}
	return string(data)
}

// Der Disziplin-Export liefert den Wertungsbogen mit den Einzelnoten – auch
// für Gäste ohne Login (reines Lese-Recht).
func TestExportProtocolPDF(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st, 7)
	addDancer(t, st, did, "1", "Erste Garde", []int{100, 90, 80, 70, 60, 50, 45})
	addDancer(t, st, did, "2", "Zweite Garde", []int{90, 90, 90}) // unvollständig

	w := getURL(t, app, fmt.Sprintf("/export/protocol?s=%d&t=%d&d=%d", sid, cid, did))
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
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "ergebnisprotokoll-garde.pdf") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	// Die Werte stecken als CSV im PDF (Dokument-Anhang).
	csv := embeddedCSV(t, body)
	if !strings.HasPrefix(csv, "\ufeffdisziplin;startnr;name;verein;stern;R1;R2") {
		t.Fatalf("CSV-Anhang fehlt oder falsch: %.120q", csv)
	}
	if !strings.Contains(csv, "Garde;1;Erste Garde") {
		t.Fatalf("CSV-Anhang ohne Tänzer: %.200q", csv)
	}
}

// Ohne komplette Wertung entsteht weiter ein PDF – mit Hinweistext statt
// Tabelle (der Knopf soll nie ins Leere führen).
func TestExportProtocolEmpty(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st, 7)
	addDancer(t, st, did, "1", "Erste Garde", []int{80, 80}) // unfertig

	w := getURL(t, app, fmt.Sprintf("/export/protocol?s=%d&t=%d&d=%d", sid, cid, did))
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
	if w := getURL(t, app, "/export/protocol?d=999"); w.Code != 404 {
		t.Fatalf("Status %d, want 404", w.Code)
	}
}

// Der Turnier-Export hat ein Blatt je Disziplin – hier: zwei Disziplinen,
// also zwei Spaltenköpfe und zwei Seiten.
func TestExportCompetitionPDF(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st, 7)
	did2, err := st.CreateDiscipline(cid, model.Discipline{Name: "Solo", NumJudges: 7})
	if err != nil {
		t.Fatal(err)
	}
	addDancer(t, st, did, "1", "Erste Garde", []int{100, 90, 80, 70, 60, 50, 45})
	addDancer(t, st, did2, "2", "Solo Lena", []int{70, 71, 72, 73, 74, 75, 76})

	w := getURL(t, app, fmt.Sprintf("/export/competition?s=%d&t=%d", sid, cid))
	if w.Code != 200 {
		t.Fatalf("Status %d, Body: %.200q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Erste Garde", "Solo Lena"} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s fehlt im Turnier-PDF", want)
		}
	}
	if n := strings.Count(body, "(Start)Tj"); n != 2 {
		t.Fatalf("Spaltenköpfe = %d, want 2 (ein Blatt je Disziplin)", n)
	}
	if !strings.Contains(body, "von 2") {
		t.Fatal("Fußzeile ohne Seitenzahl-Lösung \"von 2\"")
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "ergebnisprotokoll-heft-turnier.pdf") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	csv := embeddedCSV(t, body)
	if !strings.Contains(csv, "Garde;1;Erste Garde") || !strings.Contains(csv, "Solo;2;Solo Lena") {
		t.Fatalf("CSV-Anhang unvollständig: %.300q", csv)
	}
}

// Disziplinen ohne komplette Wertung laufen nicht als leeres Blatt mit –
// sie werden nur als Hinweis genannt.
func TestExportCompetitionSkipsEmptyDisc(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st, 7)
	did2, err := st.CreateDiscipline(cid, model.Discipline{Name: "Solo", NumJudges: 7})
	if err != nil {
		t.Fatal(err)
	}
	addDancer(t, st, did, "1", "Erste Garde", []int{100, 90, 80, 70, 60, 50, 45})
	addDancer(t, st, did2, "2", "Solo Lena", []int{70, 71}) // unfertig

	w := getURL(t, app, fmt.Sprintf("/export/competition?s=%d&t=%d", sid, cid))
	body := w.Body.String()
	if strings.Contains(body, "Solo Lena") {
		t.Fatal("unfertige Disziplin steht mit Tänzen im PDF")
	}
	if !strings.Contains(body, "ohne komplette Wertung: Solo") {
		t.Fatal("Hinweis auf die unfertige Disziplin fehlt")
	}
	if n := strings.Count(body, "(Start)Tj"); n != 1 {
		t.Fatalf("Spaltenköpfe = %d, want 1", n)
	}
}

// Ein Turnier ohne jede komplette Wertung ergibt ein PDF mit Hinweistext.
// Ein unbekanntes Turnier fällt – wie die Live-Ansicht – auf das erste
// zurück; 404 gibt es nur, wenn gar kein Turnier existiert.
func TestExportCompetitionNoResults(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, _ := seedProtocolDisc(t, st, 7)
	if w := getURL(t, app, fmt.Sprintf("/export/competition?s=%d&t=%d", sid, cid)); w.Code != 200 {
		t.Fatalf("Status %d, Body: %.200q", w.Code, w.Body.String())
	} else if !strings.Contains(w.Body.String(), "keine Disziplin mit kompletter Wertung") {
		t.Fatal("Hinweistext fehlt")
	}
	for _, url := range []string{"/export/competition?t=999", "/export/competition"} {
		if w := getURL(t, app, url); w.Code != 200 {
			t.Fatalf("%s: Status %d, want 200 (Fallback aufs erste Turnier)", url, w.Code)
		}
	}

	leer, _ := newTestApp(t, AuthConfig{})
	for _, url := range []string{"/export/competition", "/export/competition.csv"} {
		if w := getURL(t, leer, url); w.Code != 404 {
			t.Fatalf("%s ohne Daten: Status %d, want 404", url, w.Code)
		}
	}
}

// Die CSV-Routen liefern denselben Inhalt wie der Anhang im PDF.
func TestExportCSVFiles(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did := seedProtocolDisc(t, st, 7)
	addDancer(t, st, did, "1", "Erste Garde", []int{100, 90, 80, 70, 60, 50, 45})
	addDancer(t, st, did, "2", "Zweite Garde", []int{90, 90, 90, 90, 90, 90, 90})

	w := getURL(t, app, fmt.Sprintf("/export/protocol.csv?s=%d&t=%d&d=%d", sid, cid, did))
	if w.Code != 200 {
		t.Fatalf("Status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	lines := csvLines(t, w.Body.String())
	if len(lines) != 3 { // Kopf + zwei Tänze
		t.Fatalf("Zeilen = %d, want 3: %q", len(lines), w.Body.String())
	}
	if lines[0] != "disziplin;startnr;name;verein;stern;R1;R2;R3;R4;R5;R6;R7;gesamt;platz;anmerkung" {
		t.Fatalf("Kopfzeile = %q", lines[0])
	}
	// Streichregel: tiefste und höchste Note counten nicht (45 und 100),
	// deshalb 350 statt 495 – und Platz 2 hinter der Zweiten Garde.
	if want := "Garde;1;Erste Garde;;;100;90;80;70;60;50;45;350;2;gestrichen: R1,R7"; lines[1] != want {
		t.Fatalf("Zeile 1 = %q, want %q", lines[1], want)
	}
	if want := "Garde;2;Zweite Garde;;;90;90;90;90;90;90;90;450;1;gestrichen: R1,R2"; lines[2] != want {
		t.Fatalf("Zeile 2 = %q, want %q", lines[2], want)
	}

	// Turnier-CSV: beide Disziplinen, die Disziplinspalte unterscheidet sie.
	did2, err := st.CreateDiscipline(cid, model.Discipline{Name: "Solo", NumJudges: 5})
	if err != nil {
		t.Fatal(err)
	}
	addDancer(t, st, did2, "3", "Solo Lena", []int{70, 71, 72, 73, 74})
	w = getURL(t, app, fmt.Sprintf("/export/competition.csv?s=%d&t=%d", sid, cid))
	lines = csvLines(t, w.Body.String())
	if len(lines) != 4 {
		t.Fatalf("Zeilen = %d, want 4: %q", len(lines), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Garde;1;Erste Garde") ||
		!strings.Contains(w.Body.String(), "Solo;3;Solo Lena;") {
		t.Fatalf("Turnier-CSV unvollständig: %.300q", w.Body.String())
	}
	// Weniger Richter in einer Disziplin → leere Notenspalten, nichts verrutscht.
	for _, line := range lines[1:] {
		if n := len(strings.Split(line, ";")); n != 15 {
			t.Fatalf("%q hat %d Felder, want 15", line, n)
		}
	}
	if last := lines[3]; !strings.HasSuffix(last, ";70;71;72;73;74;;;216;1;gestrichen: R1,R5") {
		t.Fatalf("Solo-Zeile = %q", last)
	}
}

// csvLines zerlegt den CSV-Text in Zeilen (BOM und das abschließende
// Newline sind kein Dateninhalt).
func csvLines(t *testing.T, text string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, "\ufeff"), "\n"), "\n")
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
	// Dateiname ohne Endung, ASCII und ohne Sonderzeichen.
	if got := pdfFileName("Jugend Garde"); got != "ergebnisprotokoll-jugend-garde" {
		t.Fatalf("pdfFileName = %q", got)
	}
	if got := pdfFileName("Ü15 Solo / Gemischt"); got != "ergebnisprotokoll-ue15-solo-gemischt" {
		t.Fatalf("pdfFileName = %q", got)
	}
	if got := pdfFileName(""); got != "ergebnisprotokoll-export" {
		t.Fatalf("pdfFileName = %q", got)
	}
}

// Die Export-Links müssen in der Live-Ansicht mitgerendert werden – sonst
// ist der Export zwar da, aber nirgends zu klicken.
func TestExportLinksRender(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	seedLive(t, st)
	w := getURL(t, app, "/")
	if w.Code != 200 {
		t.Fatalf("Status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`href="/export/protocol?s=1&amp;t=1&amp;d=1"`,
		`href="/export/protocol.csv?s=1&amp;t=1&amp;d=1"`,
		`href="/export/competition?s=1&amp;t=1"`,
		`href="/export/competition.csv?s=1&amp;t=1"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Link %s fehlt in der Live-Ansicht\n%.400s", want, body)
		}
	}
}
