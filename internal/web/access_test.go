// access_test.go: Zugangs-Passwörter. -password = Lese-Zugang (Gate für
// Live-Routen), -score-password = Bewertungs-Passwort (össnet zusätzlich
// die Noteneingabe, gemeinsamer Zugang statt Login). Bei konfiguriertem
// Provider zeigt die Zugangsseite zusätzlich den OAuth-Login-Knopf.
// newTestApp lässt bei gesetztem Password den Provider leer, das ist hier
// der Regelfall (providerloser Start).

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
)

// postAccess schickt das Passwort-Formular (_like_ ein Browser nach
// Eingabe) und liefert die Antwort.
func postAccess(mux http.Handler, pw string) *http.Response {
	req := httptest.NewRequest("POST", "/access",
		strings.NewReader("password="+pw))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Result()
}

// accessCookieVal holt den Wert des Zugangsc-Cookies aus einer Antwort
// ("" = keins gesetzt).
func accessCookieVal(res *http.Response) string {
	for _, c := range res.Cookies() {
		if c.Name == accessCookie && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// postScore feuert eine Note auf Teilnehmer 1 (seedLive) und liefert die
// Antwort; cookie = access-Cookie-Wert ("" = ohne).
func postScore(mux http.Handler, cookie string) *http.Response {
	req := httptest.NewRequest("POST", "/score",
		strings.NewReader("participant_id=1&judge=1&value=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: accessCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Result()
}

// TestAccessGate: mit Passwort sind Live-Ansicht und Exporte erst nach
// korrekter Eingabe erreichbar; ohne Passwort bleibt alles offen.
func TestAccessGate(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{Password: "geheim"})
	seedLive(t, st)
	mux := app.Handler()

	// Anonymous ohne Zugang → Redirect aufs Formular, kein Seiteninhalt.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusSeeOther ||
		!strings.HasPrefix(w.Header().Get("Location"), "/access") {
		t.Fatalf("GET / ohne Zugang: Status %d Location %q",
			w.Code, w.Header().Get("Location"))
	}

	// Export (read-only) ist ebenso geschützt.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/export/protocol", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("GET /export/protocol ohne Zugang: Status %d", w.Code)
	}

	// HTMX-Polling: HX-Redirect statt Pollen gegen eine Redirect-Kette.
	req := httptest.NewRequest("GET", "/partials/refresh", nil)
	req.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent ||
		!strings.HasPrefix(w.Header().Get("HX-Redirect"), "/access") {
		t.Fatalf("HX-Polling ohne Zugang: Status %d Header %q",
			w.Code, w.Header().Get("HX-Redirect"))
	}

	// Formular erreichbar.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/access", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="password"`) {
		t.Fatalf("GET /access: Status %d, Formular fehlt", w.Code)
	}

	// Falsches Passwort: kein Cookie, Hinweis.
	res := postAccess(mux, "falsch")
	if res.StatusCode != http.StatusUnauthorized || accessCookieVal(res) != "" {
		t.Fatalf("falsches Passwort: Status %d", res.StatusCode)
	}

	// Richtiges Passwort: Cookie setzen, danach ist die Seite da.
	res = postAccess(mux, "geheim")
	val := accessCookieVal(res)
	if res.StatusCode != http.StatusSeeOther || val == "" {
		t.Fatalf("richtiges Passwort: Status %d, Cookie fehlt", res.StatusCode)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: accessCookie, Value: val})
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Testgarde") {
		t.Fatalf("GET / mit Zugang: Status %d, Inhalt fehlt", w.Code)
	}

	// Selbstgebauter Cookie (Signatur ohne Server-Secret) reicht nicht.
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: accessCookie,
		Value: "eyJlIjo5OTk5OTk5OTk5OX0=.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("GET / mit gefälschtem Zugang: Status %d", w.Code)
	}
}

// TestAccessRateLimit: nach pwMaxFails Fehlversuchen werden weitere
// Eingaben pro IP ignoriert – auch mit richtigem Passwort.
func TestAccessRateLimit(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{Password: "geheim"})
	seedLive(t, st)
	mux := app.Handler()

	for i := 0; i < pwMaxFails; i++ {
		if res := postAccess(mux, "falsch"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Fehlversuch %d: Status %d", i+1, res.StatusCode)
		}
	}
	res := postAccess(mux, "geheim")
	if res.StatusCode != http.StatusTooManyRequests || accessCookieVal(res) != "" {
		t.Fatalf("nach %d Fehlversuchen: Status %d, Cookie %q",
			pwMaxFails, res.StatusCode, accessCookieVal(res))
	}
}

// TestSessionBypassesGate: eine gültige Scorer-Session darf die
// Live-Ansicht ohne Passwort-Eingabe sehen (sonst müsste sich das
// Wertungspersonal doppelt ausweisen).
func TestSessionBypassesGate(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{Password: "geheim"})
	seedLive(t, st)
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	uid, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "test", ProviderSub: "j1",
		Email: "judge@x.de", Name: "Jutta", Role: model.RoleScorer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("sess-token", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	mux := app.Handler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "sess-token"})
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Scorer-Session ohne Zugang: Status %d", w.Code)
	}
}

// TestPasswordOnlyStart: ohne Provider (nur -password) startet die App;
// /auth/login und Schreib-Requests laufen aufs Zugangs-Formular statt
// Provider-Kontakt zu versuchen, und die Live-Ansicht bleibt geschützt.
func TestPasswordOnlyStart(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{Password: "geheim"})
	seedLive(t, st)
	mux := app.Handler()

	// /auth/login: kein Discovery-Versuch, weiter zum Passwort.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/auth/login", nil))
	if w.Code != http.StatusSeeOther ||
		!strings.HasPrefix(w.Header().Get("Location"), "/access") {
		t.Fatalf("GET /auth/login ohne Provider: Status %d Location %q",
			w.Code, w.Header().Get("Location"))
	}

	// Schreibender Request: gleiche Richtung (loginPath = /access).
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/score",
		strings.NewReader("participant_id=1&judge=1&value=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther ||
		!strings.HasPrefix(w.Header().Get("Location"), "/access") {
		t.Fatalf("POST /score ohne Provider: Status %d Location %q",
			w.Code, w.Header().Get("Location"))
	}
}

// TestNoPasswordKeepsOpenView: ohne Passwörter ist die Live-Ansicht offen
// wie vor dem Feature (Backwards-Kompatibilität).
func TestNoPasswordKeepsOpenView(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	seedLive(t, st)
	mux := app.Handler()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / ohne Passwort: Status %d, wollte 200", w.Code)
	}
	// /access ohne Passwort: kein Formular, zurück zur Ansicht.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/access", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("GET /access ohne Passwort: Status %d, wollte Redirect", w.Code)
	}
}

// TestScorePassword: das Bewertungs-Passwort öffnet die Noteneingabe, das
// reine Zugangs-Passwort nicht. Ohne jedes Recht (und ohne Login) leitet
// /score auf die Zugangsseite um.
func TestScorePassword(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{Password: "geheim", ScorePassword: "bewerten"})
	seedLive(t, st)
	mux := app.Handler()

	// Lese-Passwort: Cookie ohne Bewerten-Recht. Bewertungs-Passwort:
	// Cookie damit. Beide müssen verschieden sein (unterschiedliches
	// Payload → andere Signatur).
	readVal := accessCookieVal(postAccess(mux, "geheim"))
	scoreVal := accessCookieVal(postAccess(mux, "bewerten"))
	if readVal == "" || scoreVal == "" || readVal == scoreVal {
		t.Fatalf("Cookies fehlen oder gleich: %q / %q", readVal, scoreVal)
	}

	// Ohne Recht: Redirect auf die Zugangsseite (dort sitzt auch der
	// Provider-Button, falls einer konfiguriert ist).
	res := postScore(mux, "")
	if res.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(res.Header.Get("Location"), "/access") {
		t.Fatalf("POST /score ohne Recht: Status %d Location %q",
			res.StatusCode, res.Header.Get("Location"))
	}
	// Nur Lese-Recht: gleicher Redirect, kein Durchkommen.
	res = postScore(mux, readVal)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /score mit Lese-Cookie: Status %d Location %q",
			res.StatusCode, res.Header.Get("Location"))
	}
	// Bewerten-Cookie: Guard durchgelassen, Note wird gespeichert (200 =
	// OOB-Antwort des Score-Handlers).
	if res = postScore(mux, scoreVal); res.StatusCode != http.StatusOK {
		t.Fatalf("POST /score mit Bewerten-Cookie: Status %d, wollte 200",
			res.StatusCode)
	}

	// Panel: mit Bewerten-Cookie stehen die Note-Eingabefelder drin, mit
	// dem Lese-Cookie nur der Hinweis mit Link auf die Zugangsseite.
	get := func(cookie string) string {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{Name: accessCookie, Value: cookie})
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /: Status %d", w.Code)
		}
		return w.Body.String()
	}
	body := get(scoreVal)
	if !strings.Contains(body, `hx-post="/score"`) {
		t.Fatal("Bewerten-Cookie: Score-Eingabefelder fehlen")
	}
	body = get(readVal)
	if strings.Contains(body, `hx-post="/score"`) {
		t.Fatal("Lese-Cookie: Score-Eingabefelder sollten fehlen")
	}
	if !strings.Contains(body, `href="/access"`) {
		t.Fatal("Lese-Cookie: Hinweis-Link auf die Zugangsseite fehlt")
	}
}

// TestAccessPageOffersOAuth: mit konfiguriertem Provider sitzt auf der
// Zugangsseite der Login-Knopf – OAuth geht damit direkt auf der ersten
// Seite, nicht erst hinter der Lese-Ansicht.
func TestAccessPageOffersOAuth(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{
		Issuer: "https://idp.test", Password: "geheim", ScorePassword: "bewerten"})
	seedLive(t, st)
	mux := app.Handler()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/access", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /access: Status %d", w.Code)
	}
	if !strings.Contains(body, `/auth/login?next=`) {
		t.Fatal("OAuth-Login-Knopf fehlt auf der Zugangsseite")
	}
	if !strings.Contains(body, "Bewertungs-Passwort") {
		t.Fatal("Hinweis auf das Bewertungs-Passwort fehlt")
	}
}

// TestScorePasswordOnly: nur das Bewertungs-Passwort gesetzt lassen die
// Live-Ansicht offen – geändert werden darf erst nach Passworteingabe.
func TestScorePasswordOnly(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{ScorePassword: "bewerten"})
	seedLive(t, st)
	mux := app.Handler()

	// Live-Ansicht ohne alles erreichbar (kein Lese-Gate).
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / ohne Passwort: Status %d, wollte 200", w.Code)
	}
	// /score ohne Recht: weiter via /access (scoreLoginPath).
	if res := postScore(mux, ""); res.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(res.Header.Get("Location"), "/access") {
		t.Fatalf("POST /score ohne Recht: Status %d Location %q",
			res.StatusCode, res.Header.Get("Location"))
	}
	res := postAccess(mux, "bewerten")
	if res.StatusCode != http.StatusSeeOther || accessCookieVal(res) == "" {
		t.Fatalf("Bewertungs-Passwort: Status %d, Cookie fehlt", res.StatusCode)
	}
	if res = postScore(mux, accessCookieVal(res)); res.StatusCode != http.StatusOK {
		t.Fatalf("POST /score mit Bewerten-Cookie: Status %d, wollte 200",
			res.StatusCode)
	}
}
