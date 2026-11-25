// access_test.go: Zugangs-Passwort (Lese-Zugang) – Gate für die Lese-
// Routen, Formular, Rate-Limit und der providerlose Start (-password
// ohne oidc-*/oauth2-*). newTestApp lässt bei gesetztem Password den
// Provider leer, das ist hier der Regelfall.

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

// TestNoPasswordKeepsOpenView: ohne -password ist die Live-Ansicht offen
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
