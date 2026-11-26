// finished_test.go: Read-only-Markierung „Turnier beendet". Ein als beendet
// markiertes Turnier sperrt Noteneingabe und alle Bearbeitungen – für alle
// Rollen (auch Admin). Entsperrt wird nur über POST /admin/comp/finish.
// CSV-Import überspringt Zeilen beendeter Turniere.

package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
)

// seedFinished legt Session/Turnier/Disziplin/Teilnehmer an und liefert die
// IDs.
func seedFinished(t *testing.T, app *App) (sid, cid, did, pid int64) {
	t.Helper()
	var err error
	sid, err = app.store.CreateSession("2026/27")
	if err != nil {
		t.Fatal(err)
	}
	cid, err = app.store.CreateCompetition(sid, "Pokalturnier")
	if err != nil {
		t.Fatal(err)
	}
	did, err = app.store.CreateDiscipline(cid, model.Discipline{Name: "Garde"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err = app.store.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "1", Name: "Garde A"})
	if err != nil {
		t.Fatal(err)
	}
	return sid, cid, did, pid
}

// judgeSession legt eine eingeloggte Scorer-Session an (Cookie).
func judgeSession(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	uid, err := app.store.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "j", Email: "judge@x.de", Role: model.RoleScorer})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.CreateLoginSession("tok-judge", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: "tok-judge"}
}

// postForm helper für POSTs mit Session-Cookie.
func postForm(mux http.Handler, path, body string, ck *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ck != nil {
		req.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestCompFinishedBlocksScore(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	sid, cid, did, pid := seedFinished(t, app)
	ck := judgeSession(t, app)
	mux := app.Handler()

	postScore := func(judge int) *httptest.ResponseRecorder {
		return postForm(mux, "/score",
			fmt.Sprintf("participant_id=%d&judge=%d&value=50", pid, judge), ck)
	}
	scores := func() int {
		ps, err := st.ListParticipantsWithScores(did)
		if err != nil {
			t.Fatal(err)
		}
		return len(ps[0].Scores)
	}
	getLive := func() *httptest.ResponseRecorder {
		// Disziplin explizit anwählen, sonst rendert kein Panel.
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/?s=%d&t=%d&d=%d", sid, cid, did), nil)
		req.AddCookie(ck)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Ohne Markierung: Noteneingabe läuft und Panel zeigt Eingabefelder.
	if w := postScore(1); w.Code != http.StatusOK {
		t.Fatalf("POST /score offen: Status %d, wollte 200", w.Code)
	}
	if scores() != 1 {
		t.Fatalf("Note wurde nicht gespeichert (%d Noten)", scores())
	}
	if body := getLive().Body.String(); !strings.Contains(body, `hx-post="/score"`) {
		t.Fatal("offenes Turnier zeigt kein Eingabefeld (Test-Voraussetzung)")
	}

	// Markierung setzen: alles zum Turnier ist read-only.
	if err := st.SetCompetitionFinished(cid, true); err != nil {
		t.Fatal(err)
	}
	if w := postScore(2); w.Code != http.StatusForbidden {
		t.Fatalf("POST /score im beendet-Turnier: Status %d, wollte 403", w.Code)
	}
	if scores() != 1 {
		t.Fatal("Note wurde trotz Sperre gespeichert")
	}
	body := getLive().Body.String()
	if strings.Contains(body, `hx-post="/score"`) {
		t.Fatal("Panel zeigt trotz Sperre Eingabefelder")
	}
	if !strings.Contains(body, "Turnier beendet") {
		t.Fatal("Hinweis auf Sperre fehlt im Panel")
	}

	// Markierung weg: sofort wieder editierbar.
	if err := st.SetCompetitionFinished(cid, false); err != nil {
		t.Fatal(err)
	}
	if w := postScore(2); w.Code != http.StatusOK {
		t.Fatalf("POST /score nach Entsperrung: Status %d, wollte 200", w.Code)
	}
	if scores() != 2 {
		t.Fatal("Note nach Entsperrung nicht gespeichert")
	}
}

func TestCompFinishedBlocksAdminEdits(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	sid, cid, did, pid := seedFinished(t, app)
	if err := st.SetScore(pid, 1, 77); err != nil {
		t.Fatal(err)
	}
	ck := adminSession(t, app, "boss@x.de")
	mux := app.Handler()

	if err := st.SetCompetitionFinished(cid, true); err != nil {
		t.Fatal(err)
	}

	// Teilnehmer-Update abgelehnt, Daten unverändert. (Der String „gesperrt"
	// steht auch normal im Lösch-Bestellungstext, daher nach dem
	// eindeutigen Flash-Text „ist beendet" suchen.)
	w := postForm(mux, "/admin/part/update",
		fmt.Sprintf("id=%d&start_nr=9&name=NEU&verein=x", pid), ck)
	if !strings.Contains(w.Body.String(), "ist beendet") {
		t.Fatal("Teilnehmer-Update wurde nicht mit Sperre quittiert")
	}
	p, _ := st.GetParticipant(pid)
	if p.Name != "Garde A" {
		t.Fatalf("Name geändert trotz Sperre: %q", p.Name)
	}

	// Verschieben und Noten-Reset ebenfalls gesperrt.
	if w = postForm(mux, "/admin/part/move", fmt.Sprintf("id=%d&dir=1", pid), ck); !strings.Contains(w.Body.String(), "ist beendet") {
		t.Fatal("Move wurde nicht abgelehnt")
	}
	if w = postForm(mux, "/admin/scores/reset", fmt.Sprintf("disc_id=%d", did), ck); !strings.Contains(w.Body.String(), "ist beendet") {
		t.Fatal("Noten-Reset wurde nicht abgelehnt")
	}
	after, err := st.ListParticipantsWithScores(did)
	if err != nil || len(after) != 1 || len(after[0].Scores) != 1 {
		t.Fatalf("Noten durch Reset-Sperre verändert: %+v", after)
	}

	// Admin-Seite: Editierformulare weg, Leseliste da.
	req := httptest.NewRequest("GET",
		fmt.Sprintf("/admin?s=%d&t=%d&d=%d", sid, cid, did), nil)
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, `hx-post="/admin/part/update"`) {
		t.Fatal("Editierformulare trotz Sperre sichtbar")
	}
	if !strings.Contains(body, "Garde A") {
		t.Fatal("Lesende Teilnehmeransicht fehlt")
	}

	// Sperre aufheben über die UI-Route …
	if w = postForm(mux, "/admin/comp/finish", fmt.Sprintf("id=%d&on=0", cid), ck); !strings.Contains(w.Body.String(), "wieder editierbar") {
		t.Fatal("Entsperren über UI fehlgeschlagen")
	}
	// … danach darf wieder editiert werden (kein Sperre-Hinweis mehr,
	// Name wird geändert).
	if w = postForm(mux, "/admin/part/update",
		fmt.Sprintf("id=%d&start_nr=1&name=NEU&verein=x", pid), ck); strings.Contains(w.Body.String(), "ist beendet") {
		t.Fatal("Update nach Entsperren weiterhin gesperrt")
	}
	p, _ = st.GetParticipant(pid)
	if p.Name != "NEU" {
		t.Fatal("Update nach Entsperren nicht angewendet")
	}
}

func TestCompFinishToggleOnlyByAdmin(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	_, cid, _, _ := seedFinished(t, app)
	mux := app.Handler()

	// Admin schaltet die Sperre über die neue Route ein …
	ck := adminSession(t, app, "boss@x.de")
	if w := postForm(mux, "/admin/comp/finish", fmt.Sprintf("id=%d&on=1", cid), ck); w.Code != http.StatusOK {
		t.Fatalf("Admin-Toggle: Status %d", w.Code)
	}
	c, _ := st.GetCompetition(cid)
	if !c.Finished {
		t.Fatal("Markierung über UI nicht gesetzt")
	}
	// … anonyme Anfragen dürfen das nicht (sonst wäre die Sperre sinnlos).
	if w := postForm(mux, "/admin/comp/finish", "id=1&on=0", nil); w.Code == http.StatusOK &&
		strings.Contains(w.Body.String(), "wieder editierbar") {
		t.Fatal("anonyme Entsperrung durchgelassen")
	}
	c, _ = st.GetCompetition(cid)
	if !c.Finished {
		t.Fatal("anonym entsperrt!")
	}
}

func TestCSVImportSkipsFinished(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, _, pid := seedFinished(t, app)
	if err := st.SetCompetitionFinished(cid, true); err != nil {
		t.Fatal(err)
	}
	csv := "2026/27;Pokalturnier;Garde;1;NEUER NAME;V;0;0\n" +
		"2026/27;Neues Ding;Garde;7;Neue Gruppe;VV;0;0\n"
	imported, skipped, err := st.ImportCSV(csv)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || skipped != 1 {
		t.Fatalf("importiert=%d übersprungen=%d, wollten 1/1", imported, skipped)
	}
	p, err := st.GetParticipant(pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Garde A" {
		t.Fatalf("Name im beendeten Turnier überschrieben: %q", p.Name)
	}
	// Das neue Turnier wurde angelegt und enthält die neue Gruppe.
	comps, err := st.ListCompetitions(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 2 {
		t.Fatalf("Turniere nach Import: %d, wollten 2", len(comps))
	}
	var newCID int64
	for _, c := range comps {
		if c.ID != cid {
			newCID = c.ID
		}
	}
	discs, _ := st.ListDisciplines(newCID)
	if len(discs) != 1 {
		t.Fatalf("Disziplinen im neuen Turnier: %d", len(discs))
	}
	parts, _ := st.ListParticipants(discs[0].ID)
	if len(parts) != 1 || parts[0].Name != "Neue Gruppe" {
		t.Fatalf("neue Gruppe fehlt: %+v", parts)
	}
	// Herkömmlicher Import in offenes Turnier zählt vollständig.
	if n, s, err := st.ImportCSV("2026/27;Neues Ding;Garde;8;Noch eine;VV;0;0\n"); err != nil || n != 1 || s != 0 {
		t.Fatalf("Import ins offene Turnier: n=%d s=%d err=%v", n, s, err)
	}
}
