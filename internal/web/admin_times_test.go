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
)

// adminSession legt eine eingeloggte Admin-Session an und liefert das Cookie.
func adminSession(t *testing.T, app *App, email string) *http.Cookie {
	t.Helper()
	uid, err := app.store.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "s", Email: email, Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.CreateLoginSession("tok-admin", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: "tok-admin"}
}

func TestAdminCompTimes(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	seedLive(t, st)
	ck := adminSession(t, app, "boss@x.de")
	mux := app.Handler()

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID

	// Startzeit 09:30 + Pause 10 min speichern.
	form := url.Values{
		"id":        {strconv.FormatInt(cid, 10)},
		"start_at":  {"09:30"},
		"break_min": {"10"},
	}
	req := httptest.NewRequest("POST", "/admin/comp/times", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /admin/comp/times: Status %d, wollte 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Turnier-Zeiten gespeichert") {
		t.Fatal("Flash-Nachricht fehlt")
	}
	c, err := st.GetCompetition(cid)
	if err != nil {
		t.Fatal(err)
	}
	if c.StartAtSec != 9*3600+30*60 {
		t.Fatalf("StartAtSec = %d, want %d", c.StartAtSec, 9*3600+30*60)
	}
	if c.BreakSec != 10*60 {
		t.Fatalf("BreakSec = %d, want %d", c.BreakSec, 600)
	}

	// Leere Startzeit = nicht gesetzt (0).
	form = url.Values{
		"id":        {strconv.FormatInt(cid, 10)},
		"start_at":  {""},
		"break_min": {"0"},
	}
	req = httptest.NewRequest("POST", "/admin/comp/times", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST (leer): Status %d, wollte 200", w.Code)
	}
	c, _ = st.GetCompetition(cid)
	if c.StartAtSec != 0 || c.BreakSec != 0 {
		t.Fatalf("StartAtSec/BreakSec = %d/%d, want 0/0", c.StartAtSec, c.BreakSec)
	}

	// Ungültiges Format wird abgewiesen.
	form = url.Values{
		"id":        {strconv.FormatInt(cid, 10)},
		"start_at":  {"9:75"},
		"break_min": {"0"},
	}
	req = httptest.NewRequest("POST", "/admin/comp/times", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "Ungültige Startzeit") {
		t.Fatal("Ungültige Startzeit wird nicht abgewiesen")
	}
}

func TestLiveShowsPlanAndNextDiscipline(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	seedLive(t, st)
	ck := adminSession(t, app, "boss@x.de")
	mux := app.Handler()

	sessions, _ := st.ListSessions()
	comps, _ := st.ListCompetitions(sessions[0].ID)
	cid := comps[0].ID
	// Zweite Disziplin mit einem Teilnehmer (für "nächste Disziplin").
	if _, err := st.CreateDiscipline(cid, model.Discipline{Name: "Paare"}); err != nil {
		t.Fatal(err)
	}
	discs, _ := st.ListDisciplines(cid)
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: discs[1].ID, StartNr: "1", Name: "Testpaar"}); err != nil {
		t.Fatal(err)
	}

	// Startzeit setzen (23:55: die 5-Minuten-Disziplin läuft über
	// Mitternacht, die nächste Disziplin startet am Folgetag um 00:10 –
	// unabhängig von der Testlaufzeit garantiert in der Zukunft) + Pause.
	form := url.Values{
		"id":        {strconv.FormatInt(cid, 10)},
		"start_at":  {"23:55"},
		"break_min": {"10"},
	}
	req := httptest.NewRequest("POST", "/admin/comp/times", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /admin/comp/times: Status %d, wollte 200", w.Code)
	}

	// Admin-Seite zeigt die eingegebenen Werte.
	req = httptest.NewRequest("GET", "/admin?s=1", nil)
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `value="23:55"`) || !strings.Contains(body, `value="10"`) {
		t.Fatal("Admin-Seite zeigt Startzeit/Pause nicht korrekt an")
	}

	// Live-Seite (erste Disziplin): Turnier-Start + Disziplin-Plan.
	req = httptest.NewRequest("GET", "/?s=1&t=1&d=1", nil)
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, "Turnier-Start: <b>23:55</b>") {
		t.Fatal("Live-Ansicht: Turnier-Start fehlt")
	}
	if !strings.Contains(body, "Pause: <b>10:00</b>") {
		t.Fatal("Live-Ansicht: Pause fehlt")
	}
	if !strings.Contains(body, "class=\"plan-discs\"") || !strings.Contains(body, "Garde 23:55") ||
		!strings.Contains(body, "Paare 00:10") {
		t.Fatal("Live-Ansicht: Disziplin-Plan fehlt (Garde 23:55, Paare 00:10)")
	}
	if !strings.Contains(body, "Nächste Disziplin: <b>Paare</b>") {
		t.Fatal("Live-Ansicht: nächste Disziplin fehlt")
	}
}
