package web

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"turnier/internal/model"
	"turnier/internal/store"
)

// ---------- Test-Setup ----------

// newTestApp baut eine App mit echter SQLite (Temp-DB), echten Templates
// (web/ aus dem Repo) und Auth – ohne Provider-Kontakt (Discovery ist lazy).
func newTestApp(t *testing.T, cfg AuthConfig) (*App, *store.Store) {
	t.Helper()
	// Repo-Root: die Template-Pattern sind relativ dazu (web/templates/…).
	assets := os.DirFS(filepath.Join("..", ".."))
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if cfg.Issuer == "" && cfg.OAuth2BaseURL == "" {
		cfg.Issuer = "https://idp.test"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "turnier"
	}
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = "secret"
	}
	if cfg.RedirectURL == "" {
		cfg.RedirectURL = "http://turnier.test/auth/callback"
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = time.Hour
	}
	auth, err := NewAuth(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(st, assets, auth)
	if err != nil {
		t.Fatal(err)
	}
	return app, st
}

// giveRole seedet einen Rollen-Eintrag direkt in die DB (das Äquivalent
// zu -admins beim Start bzw. dem Hinzufügen in der Admin-UI).
func giveRole(t *testing.T, st *store.Store, role, email string) {
	t.Helper()
	if err := st.AddLoginRole(role, email); err != nil {
		t.Fatal(err)
	}
}

func cookieFrom(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("Cookie %s nicht in Set-Cookie gefunden", name)
	return nil
}

// decodeStatePayload entpackt das signierte State-Cookie (Test-Zwecke,
// gleiche App-Instanz → Signatur wird akzeptiert).
func (a *App) decodeStatePayload(t *testing.T, v string) statePayload {
	t.Helper()
	p, err := a.auth.verifyPayload(v)
	if err != nil {
		t.Fatalf("State-Cookie dekodieren: %v", err)
	}
	return *p
}

// ---------- Rollen-Guards ----------

func TestRoleGuards(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	mux := app.Handler()

	post := func(path, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path,
			strings.NewReader("participant_id=1&judge=1&value=50"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	get := func(path, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// 1. Nicht angemeldet: /score und /admin → Redirect zum Login (mit next).
	w := post("/score", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /score ohne Login: Status %d, wollte 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/auth/login?next=") {
		t.Fatalf("Redirect nach %q, wollte /auth/login?next=…", loc)
	}
	if w = get("/admin", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("GET /admin ohne Login: Status %d, wollte 303", w.Code)
	}

	// Sessions für Admin und Scorer anlegen.
	uidAdmin, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "a", Email: "boss@x.de", Name: "Boss", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	uidScorer, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "s", Email: "judge@x.de", Name: "Judge", Role: model.RoleScorer})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok-admin", uidAdmin, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok-scorer", uidScorer, time.Hour); err != nil {
		t.Fatal(err)
	}

	// 2. Scorer: /score wird durchgelassen (404 = kein Teilnehmer, aber
	//    NICHT der Login-Redirect), /admin → 403.
	w = post("/score", "tok-scorer")
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /score als Scorer: Status %d, wollte 404 (Guard durch)", w.Code)
	}
	if w = get("/admin", "tok-scorer"); w.Code != http.StatusForbidden {
		t.Fatalf("GET /admin als Scorer: Status %d, wollte 403", w.Code)
	}

	// 3. Admin: /admin → 200, /score → 404 (durch).
	if w = get("/admin", "tok-admin"); w.Code != http.StatusOK {
		t.Fatalf("GET /admin als Admin: Status %d, wollte 200", w.Code)
	}
	if w = post("/score", "tok-admin"); w.Code != http.StatusNotFound {
		t.Fatalf("POST /score als Admin: Status %d, wollte 404 (Guard durch)", w.Code)
	}

	// 4. Rolle entzogen (Adresse nicht mehr in der Rollen-Liste): Session wird
	//    verworfen → Redirect zum Login (keine 403-Schleife).
	uidKicked, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "k", Email: "kicked@x.de", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok-kicked", uidKicked, time.Hour); err != nil {
		t.Fatal(err)
	}
	w = post("/score", "tok-kicked")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /score mit entzogener Rolle: Status %d, wollte 303", w.Code)
	}
	if _, err := st.GetLoginUserBySession("tok-kicked"); err == nil {
		t.Fatal("Session der entzogenen Adresse sollte gelöscht sein")
	}

	// 5. Live-Ansicht bleibt für alle lesbar (auch ohne Login);
	//    hier 404, weil die Test-DB leer ist ("keine Disziplin vorhanden").
	if w = get("/", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET / ohne Login: Status %d, wollte 404 (leere DB)", w.Code)
	}

	// 6. HTMX-Request ohne Login: HX-Redirect statt 303 (Page-Navigation,
	//    z. B. abgelaufene Session mitten im Turnier).
	req := httptest.NewRequest("POST", "/score",
		strings.NewReader("participant_id=1&judge=1&value=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST /score (HTMX) ohne Login: Status %d, wollte 204", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); !strings.HasPrefix(loc, "/auth/login?next=") {
		t.Fatalf("HX-Redirect: %q, wollte /auth/login?next=…", loc)
	}
}

func TestRoleFor(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "a@x.de")
	giveRole(t, st, model.RoleScorer, "s@x.de")
	if got := app.auth.RoleFor("A@x.de"); got != model.RoleAdmin {
		t.Fatalf("RoleFor(admin): %q", got)
	}
	if got := app.auth.RoleFor("s@x.de"); got != model.RoleScorer {
		t.Fatalf("RoleFor(scorer): %q", got)
	}
	if got := app.auth.RoleFor("other@x.de"); got != "" {
		t.Fatalf("RoleFor(fremd): %q, wollte leere Rolle", got)
	}
	// Admin schlägt Scorer (Adresse in beiden Listen).
	app2, st2 := newTestApp(t, AuthConfig{})
	giveRole(t, st2, model.RoleAdmin, "both@x.de")
	giveRole(t, st2, model.RoleScorer, "both@x.de")
	if got := app2.auth.RoleFor("both@x.de"); got != model.RoleAdmin {
		t.Fatalf("RoleFor(both): %q, wollte admin", got)
	}
}

// ---------- State-Payload ----------

func TestStatePayload(t *testing.T) {
	app, _ := newTestApp(t, AuthConfig{})
	a := app.auth

	p := statePayload{State: "s1", Nonce: "n1", Verifier: "v1",
		Next: "/admin", Exp: time.Now().Add(time.Minute).Unix()}
	v, err := a.signPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.verifyPayload(v)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "s1" || got.Nonce != "n1" || got.Verifier != "v1" || got.Next != "/admin" {
		t.Fatalf("Payload-Abweichung: %+v", got)
	}
	// Tampering: signiertes Feld geändert.
	if _, err := a.verifyPayload(v[:len(v)-2] + "AA"); err == nil {
		t.Fatal("verändertes Payload sollte verworfen werden")
	}
	// Abgelaufen.
	old, _ := a.signPayload(statePayload{State: "s", Exp: time.Now().Add(-time.Minute).Unix()})
	if _, err := a.verifyPayload(old); err == nil {
		t.Fatal("abgelaufenes Payload sollte verworfen werden")
	}
}

// ---------- Vollständiger OIDC-Flow mit Fake-Provider ----------

// fakeProvider ist ein minimaler OIDC-Server (Discovery, JWKS, Auth, Token)
// mit RS256-signierten ID-Tokens.
type fakeProvider struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	email    string
	name     string
	authQ    url.Values // letzter /auth-Request
}

func newFakeProvider(t *testing.T, clientID, email, name string) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{key: key, clientID: clientID, email: email, name: name}
	mux := http.NewServeMux()
	b64 := base64.RawURLEncoding

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"userinfo_endpoint": %q,
			"jwks_uri": %q,
			"scopes_supported": ["openid","profile","email"],
			"id_token_signing_alg_values_supported": ["RS256"]
		}`, fp.srv.URL, fp.srv.URL+"/auth", fp.srv.URL+"/token",
			fp.srv.URL+"/userinfo", fp.srv.URL+"/keys")
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := key.PublicKey
		e := make([]byte, 4)
		binary.BigEndian.PutUint32(e, uint32(pub.E))
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","kid":"k1","use":"sig","alg":"RS256",
			"n":"%s","e":"%s"}]}`, b64.EncodeToString(pub.N.Bytes()), b64.EncodeToString(e))
	})
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		fp.authQ = q
		if q.Get("client_id") != fp.clientID ||
			q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "ungültige Auth-Anfrage (PKCE fehlt?)", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=fakecode&state="+q.Get("state"),
			http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f := r.PostForm
		if f.Get("client_id") != fp.clientID || f.Get("client_secret") != "secret" ||
			f.Get("grant_type") != "authorization_code" || f.Get("code") != "fakecode" ||
			f.Get("code_verifier") == "" {
			http.Error(w, "ungültige Token-Anfrage: "+f.Encode(), http.StatusBadRequest)
			return
		}
		id, err := fp.signIDToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","id_token":%q}`, id)
	})
	fp.srv = httptest.NewServer(mux)
	t.Cleanup(fp.srv.Close)
	return fp
}

func (fp *fakeProvider) signIDToken() (string, error) {
	now := time.Now()
	b64 := base64.RawURLEncoding
	header := `{"alg":"RS256","kid":"k1","typ":"JWT"}`
	payload := fmt.Sprintf(
		`{"iss":%q,"sub":"user-1","aud":%q,"exp":%d,"iat":%d,"nonce":%q,
		 "email":%q,"name":%q,"email_verified":true}`,
		fp.srv.URL, fp.clientID,
		now.Add(time.Hour).Unix(), now.Add(-time.Minute).Unix(),
		fp.authQ.Get("nonce"), fp.email, fp.name)
	signed := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, fp.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + b64.EncodeToString(sig), nil
}

// startLogin führt den /auth/login-Schritt aus und liefert den
// Provider-Auth-URL plus das State-Cookie.
func startLogin(t *testing.T, mux http.Handler, next string) (url.URL, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/login?next="+url.QueryEscape(next), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Login-Start: Status %d, wollte 303", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return *u, cookieFrom(t, w, stateCookie)
}

// providerAuthorize simuliert den Browser: GET auf die Authorize-URL des
// Providers (der merkt sich u. a. die Nonce für das spätere ID-Token).
func providerAuthorize(t *testing.T, authURL url.URL) {
	t.Helper()
	provReq, err := http.NewRequest("GET", authURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	provResp, err := client.Do(provReq)
	if err != nil {
		t.Fatalf("Provider-Authorize: %v", err)
	}
	provResp.Body.Close()
	if provResp.StatusCode != http.StatusFound {
		t.Fatalf("Provider-Authorize: Status %d, wollte 302", provResp.StatusCode)
	}
}

// finishLogin führt den Callback aus und erwartet eine Session.
func finishLogin(t *testing.T, mux http.Handler, authURL url.URL, sc *http.Cookie) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("GET",
		"/auth/callback?code=fakecode&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Callback: Status %d, wollte 303 (Body: %s)",
			w.Code, w.Body.String()[:min(len(w.Body.String()), 300)])
	}
	return cookieFrom(t, w, sessionCookie)
}

func TestOIDCCallbackFlow(t *testing.T) {
	email, name := "judge@x.de", "J. Ridger"
	fp := newFakeProvider(t, "turnier", email, name)
	app, st := newTestApp(t, AuthConfig{Issuer: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, email)
	mux := app.Handler()

	authURL, sc := startLogin(t, mux, "/")
	if !strings.HasPrefix(authURL.String(), fp.srv.URL+"/auth") {
		t.Fatalf("Redirect nach %q, wollte %s/auth", authURL.String(), fp.srv.URL)
	}
	q := authURL.Query()
	if q.Get("client_id") != "turnier" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("Auth-URL fehlt client_id/state/nonce: %s", authURL.String())
	}
	// PKCE: challenge im Auth-URL = S256(Verifier aus dem State-Cookie).
	payload := app.decodeStatePayload(t, sc.Value)
	if q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(payload.Verifier) {
		t.Fatal("code_challenge passt nicht zum S256-Verifier")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method: %q, wollte S256", q.Get("code_challenge_method"))
	}

	providerAuthorize(t, authURL) // Browser-Schritt: Provider merkt Nonce
	session := finishLogin(t, mux, authURL, sc)
	if session.Value == "" {
		t.Fatal("Callback hat keine Session gesetzt")
	}

	// Nutzer + Session müssen in der DB liegen.
	u, err := st.GetLoginUserBySession(session.Value)
	if err != nil {
		t.Fatalf("Session nach Callback nicht gefunden: %v", err)
	}
	if u.Email != email || u.Name != name || u.Role != model.RoleScorer {
		t.Fatalf("User-Abweichung: %+v", u)
	}

	// Mit der Session: /score wird durchgelassen, /admin 403.
	req := httptest.NewRequest("POST", "/score",
		strings.NewReader("participant_id=1&judge=1&value=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /score nach Login: Status %d, wollte 404 (Guard durch)", w.Code)
	}
	req = httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /admin als Scorer: Status %d, wollte 403", w.Code)
	}
}

// seedLive legt eine minimale Wettkampfhierarchie an (Session → Turnier →
// Disziplin → Teilnehmer), damit die Live-Ansicht rendert.
func seedLive(t *testing.T, st *store.Store) {
	t.Helper()
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
	if _, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "1", Name: "Testgarde"}); err != nil {
		t.Fatal(err)
	}
}

func TestLivePageReadonlyForGuest(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	seedLive(t, st)
	mux := app.Handler()

	// Gast: Seite rendert, Anmelden-Link da, aber KEINE Score-Formulare,
	// kein Admin-Link.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / als Gast: Status %d, wollte 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/auth/login") || !strings.Contains(body, "Nur Lese-Recht") {
		t.Fatal("Gast-Ansicht: Anmelden-Link bzw. Lese-Recht-Hinweis fehlt")
	}
	if strings.Contains(body, `hx-post="/score"`) {
		t.Fatal("Gast-Ansicht enthält Score-Formulare")
	}
	if strings.Contains(body, `href="/admin"`) {
		t.Fatal("Gast-Ansicht enthält Admin-Link")
	}

	// Scorer: Score-Formulare + Abmelden sichtbar, aber kein Admin-Link.
	uid, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "s", Email: "judge@x.de", Role: model.RoleScorer})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `hx-post="/score"`) {
		t.Fatalf("Scorer-Ansicht ohne Score-Formulare (Status %d)", w.Code)
	}
	if !strings.Contains(body, "Abmelden") || strings.Contains(body, `href="/admin"`) {
		t.Fatal("Scorer-Ansicht: Abmelden fehlt bzw. Admin-Link sichtbar")
	}
}

// TestPWAHead: Live-Seite verweist auf das Web-App-Manifest (iPhone/iPad:
// „Zum Startbildschirm“) und der Manifest-Request liefert den korrekten
// MIME-Typ – ohne den akzeptieren manche Browser das Manifest nicht.
func TestPWAHead(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	seedLive(t, st)
	mux := app.Handler()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	for _, want := range []string{
		`<link rel="manifest" href="/static/manifest.webmanifest">`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-title" content="turnier">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Head ohne: %s", want)
		}
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/static/manifest.webmanifest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/manifest.webmanifest: Status %d, wollte 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Manifest Content-Type = %q, wollte application/manifest+json", ct)
	}
}

// TestLiveWithdrawnShownButSkipped: gestrichene Teilnehmer erscheinen im
// Zeitplan (gestrichen, ohne Zeit), werden aber bei der Zeitprognose
// übersprungen – kein Slot, kein „als Nächstes“.
func TestLiveWithdrawnShownButSkipped(t *testing.T) {
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
	aID, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "1", Name: "Alice", Verein: "TG Nord"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "2", Name: "Bob", Verein: "TG Süd", Withdrawn: true})
	if err != nil {
		t.Fatal(err)
	}
	cID, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "3", Name: "Carol", Verein: "TG Ost"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	ld, err := app.loadLive(did, now)
	if err != nil {
		t.Fatal(err)
	}

	// Zeitplan-Zeilen: alle drei, in Startreihenfolge.
	if len(ld.SchedRows) != 3 {
		t.Fatalf("SchedRows: n=%d, wollte 3", len(ld.SchedRows))
	}
	if ld.SchedRows[0].StartNr != "1" || ld.SchedRows[1].StartNr != "2" ||
		ld.SchedRows[2].StartNr != "3" {
		t.Fatalf("Startreihenfolge gestört: %+v", ld.SchedRows)
	}
	// Bob (gestrichen): kein Slot, keine Zeit, nie „an der Reihe“.
	bob := ld.SchedRows[1]
	if !bob.Withdrawn || bob.StartEst != nil || bob.EndEst != nil || bob.IsCurrent {
		t.Fatalf("Gestrichener Teilnehmer trägt Zeitangaben: %+v", bob)
	}
	// Carol folgt DIREKT auf Alice – Bobs Slot zählt nicht.
	if aliceEnd := ld.SchedRows[0].EndEst; aliceEnd == nil ||
		ld.SchedRows[2].StartEst == nil || !ld.SchedRows[2].StartEst.Equal(*aliceEnd) {
		t.Fatalf("Carol sollte direkt auf Alice folgen: Alice.EndEst=%v Carol.StartEst=%v",
			ld.SchedRows[0].EndEst, ld.SchedRows[2].StartEst)
	}
	// Current = Alice, Next = Carol (Bob übersprungen).
	if ld.Sched.Current == nil || ld.Sched.Current.ID != aID ||
		ld.Sched.Next == nil || ld.Sched.Next.ID != cID {
		t.Fatalf("Current/Next falsch: %+v", ld.Sched)
	}

	// Live-Seite: Bob gestrichen sichtbar, im Punkteeingabe-Selektor
	// aber nicht wählbar.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<span class="withdrawn-name">Bob</span>`) {
		t.Fatal("gestrichener Teilnehmer ist nicht durchgestrichen angezeigt")
	}
	if !strings.Contains(body, `<span class="badge withdrawn">gestrichen</span>`) {
		t.Fatal("„gestrichen“-Badge fehlt im Zeitplan")
	}
	if n := strings.Count(body, `class="withdrawn-row"`); n != 1 {
		t.Fatalf("Gestrichene Zeitplan-Zeilen: n=%d, wollte 1", n)
	}
	if strings.Contains(body, `2 · Bob`) {
		t.Fatal("gestrichener Teilnehmer steht im Punkteeingabe-Selektor")
	}
}

// TestLiveRenderCompleteScore: eine komplette Wertung (7/7) muss die
// Rangliste rendern – Regressionstest: Die eigene Template-Funktion "or"
// (String-Fallback für den Anmeldenamen) überschrieb damals das eingebaute
// Boolean-OR und brach rank.html:26 ("or (eq …) (eq …)") ab, sobald die
// erste Zeile mit Strich-Noten renderte.
func TestLiveRenderCompleteScore(t *testing.T) {
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
	pid, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "1", Name: "Alice", Verein: "TG Nord"})
	if err != nil {
		t.Fatal(err)
	}
	for j, v := range []int{90, 95, 88, 92, 97, 85, 91} {
		if err := st.SetScore(pid, j+1, v); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: Status %d, wollte 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "Templatefehler") {
		t.Fatal("Templatefehler in der Live-Seite (komplette Wertung)")
	}
	// Summe der mittleren 5 (85 und 97 gestrichen): 88+90+91+92+95 = 456.
	if !strings.Contains(body, "456") {
		t.Fatal("Ranglistensumme fehlt")
	}
	if !strings.Contains(body, "score-dropped") {
		t.Fatal("gestrichene Mindest-/Maximalnote fehlt")
	}
	// OOB-Refresh (HTMX-Polling) muss denselben Weg fehlerfrei rendern.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/partials/refresh?s=1&t=1&d=1", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Templatefehler") {
		t.Fatal("Templatefehler im Refresh-Partial (komplette Wertung)")
	}
}

func TestOIDCCallbackNotAllowed(t *testing.T) {
	fp := newFakeProvider(t, "turnier", "stranger@x.de", "Stranger")
	app, st := newTestApp(t, AuthConfig{Issuer: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, "judge@x.de") // stranger ist nicht dabei
	mux := app.Handler()

	authURL, sc := startLogin(t, mux, "/")
	providerAuthorize(t, authURL)
	req := httptest.NewRequest("GET",
		"/auth/callback?code=fakecode&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Callback mit nicht-freigeschalteter Adresse: Status %d, wollte 403", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("für nicht-freigeschaltete Adresse darf keine Session angesetzt werden")
		}
	}
	if users, err := st.ListLoginUsers(); err != nil || len(users) != 0 {
		t.Fatalf("für abgewiesene Adresse darf kein User angelegt werden (n=%d, err=%v)",
			len(users), err)
	}
}

func TestOIDCWrongStateRejected(t *testing.T) {
	fp := newFakeProvider(t, "turnier", "judge@x.de", "J.")
	app, st := newTestApp(t, AuthConfig{Issuer: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	mux := app.Handler()

	_, sc := startLogin(t, mux, "/")
	req := httptest.NewRequest("GET", "/auth/callback?code=fakecode&state=gefake", nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Callback mit falschem State: Status %d, wollte 400", w.Code)
	}
}

func TestOIDCProviderOffline(t *testing.T) {
	// Discovery schlägt fehl (geschlossener Port) → Login-Start mit 503,
	// aber die App läuft weiter und die Live-Ansicht bleibt erreichbar.
	app, _ := newTestApp(t, AuthConfig{Issuer: "http://127.0.0.1:1"})
	mux := app.Handler()

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Login mit unerreichter Discovery: Status %d, wollte 503", w.Code)
	}
	req = httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Health-Check: Status %d, wollte 200", w.Code)
	}
}

// ---------- Generisches OAuth2 (mailcow) mit Fake-Provider ----------

// newTestStore öffnet eine Temp-DB (für Tests, die nur NewAuth prüfen).
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestNewAuthProviderModes(t *testing.T) {
	base := AuthConfig{ClientID: "turnier", ClientSecret: "secret",
		RedirectURL: "http://turnier.test/auth/callback"}

	// Weder Issuer noch Base-URL → Fehler.
	if _, err := NewAuth(newTestStore(t), base); err == nil {
		t.Fatal("ohne Provider-Modus erwartet Fehler")
	}
	// Beide gleichzeitig → Fehler.
	both := base
	both.Issuer = "https://idp.test"
	both.OAuth2BaseURL = "https://mail.example.com"
	if _, err := NewAuth(newTestStore(t), both); err == nil {
		t.Fatal("mit beiden Modi erwartet Fehler")
	}
	// OIDC-Modus bleibt intakt.
	c := base
	c.Issuer = "https://idp.test"
	a, err := NewAuth(newTestStore(t), c)
	if err != nil {
		t.Fatalf("OIDC-Modus: %v", err)
	}
	if a.generic || a.providerName != "https://idp.test" {
		t.Fatalf("OIDC-Modus: generic=%v providerName=%q", a.generic, a.providerName)
	}
	// Generischer Modus: Endpoints aus der Base-URL abgeleitet
	// (Trailing-Slash erlaubt), Scope Default "profile".
	c = base
	c.OAuth2BaseURL = "https://mail.example.com/"
	a, err = NewAuth(newTestStore(t), c)
	if err != nil {
		t.Fatalf("generischer Modus: %v", err)
	}
	if !a.generic || a.providerName != "https://mail.example.com" {
		t.Fatalf("generischer Modus: generic=%v providerName=%q", a.generic, a.providerName)
	}
	if a.authURL != "https://mail.example.com/oauth/authorize" ||
		a.conf.Endpoint.TokenURL != "https://mail.example.com/oauth/token" ||
		a.profileURL != "https://mail.example.com/oauth/profile" {
		t.Fatalf("Endpoint-Ableitung: auth=%q token=%q profile=%q",
			a.authURL, a.conf.Endpoint.TokenURL, a.profileURL)
	}
	if len(a.conf.Scopes) != 1 || a.conf.Scopes[0] != "profile" {
		t.Fatalf("Scope: %v, wollte [profile]", a.conf.Scopes)
	}
	// Einzelne Endpoint-Overrides greifen.
	c = base
	c.OAuth2BaseURL = "https://mail.example.com"
	c.OAuth2AuthorizeURL = "https://other.example.com/authorize"
	c.OAuth2TokenURL = "https://other.example.com/token"
	c.OAuth2ProfileURL = "https://other.example.com/me"
	c.OAuth2Scope = "openid profile"
	a, err = NewAuth(newTestStore(t), c)
	if err != nil {
		t.Fatal(err)
	}
	if a.authURL != "https://other.example.com/authorize" ||
		a.conf.Endpoint.TokenURL != "https://other.example.com/token" ||
		a.profileURL != "https://other.example.com/me" ||
		len(a.conf.Scopes) != 1 || a.conf.Scopes[0] != "openid profile" {
		t.Fatalf("Overrides: auth=%q token=%q profile=%q scopes=%v",
			a.authURL, a.conf.Endpoint.TokenURL, a.profileURL, a.conf.Scopes)
	}
}

// fakeOAuth2 simuliert den eingebauten OAuth2-Provider von mailcow:
// exakte Redirect-URI-Prüfung, kein Discovery, kein ID-Token, Profile-JSON.
type fakeOAuth2 struct {
	srv         *httptest.Server
	clientID    string
	redirectURI string
	email       string
	name        string
	withEmail   bool // Profile liefert identitätsfähige Felder
	tokenSeen   bool
}

func newFakeOAuth2(t *testing.T, clientID, redirectURI, email, name string) *fakeOAuth2 {
	t.Helper()
	fp := &fakeOAuth2{clientID: clientID, redirectURI: redirectURI,
		email: email, name: name, withEmail: true}
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Wie mailcow: client_id, response_type, exakte redirect_uri,
		// state und scope müssen passen.
		if q.Get("client_id") != fp.clientID ||
			q.Get("response_type") != "code" ||
			q.Get("redirect_uri") != fp.redirectURI ||
			q.Get("state") == "" || q.Get("scope") != "profile" {
			http.Error(w, "ungültige Auth-Anfrage: "+q.Encode(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fp.redirectURI+"?code=fakecode&state="+q.Get("state"),
			http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f := r.PostForm
		if f.Get("client_id") != fp.clientID || f.Get("client_secret") != "secret" ||
			f.Get("grant_type") != "authorization_code" || f.Get("code") != "fakecode" {
			http.Error(w, "ungültige Token-Anfrage: "+f.Encode(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at-1","token_type":"Bearer",
			"expires_in":3600,"scope":"profile"}`)
	})
	mux.HandleFunc("/oauth/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			http.Error(w, "ungültiges Bearer-Token", http.StatusUnauthorized)
			return
		}
		fp.tokenSeen = true
		w.Header().Set("Content-Type", "application/json")
		if !fp.withEmail {
			fmt.Fprintf(w, `{"success":true,"full_name":%q,"active":true}`, fp.name)
			return
		}
		fmt.Fprintf(w, `{"success":true,"username":%q,"id":%q,"identifier":%q,
			"email":%q,"full_name":%q,"displayName":%q,"active":true}`,
			fp.email, fp.email, fp.email, fp.email, fp.name, fp.name)
	})
	fp.srv = httptest.NewServer(mux)
	t.Cleanup(fp.srv.Close)
	return fp
}

func TestOAuth2CallbackFlow(t *testing.T) {
	email, name := "judge@x.de", "J. Ridger"
	fp := newFakeOAuth2(t, "turnier", "http://turnier.test/auth/callback", email, name)
	app, st := newTestApp(t, AuthConfig{OAuth2BaseURL: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, email)
	mux := app.Handler()

	authURL, sc := startLogin(t, mux, "/")
	if !strings.HasPrefix(authURL.String(), fp.srv.URL+"/oauth/authorize") {
		t.Fatalf("Redirect nach %q, wollte %s/oauth/authorize", authURL.String(), fp.srv.URL)
	}
	q := authURL.Query()
	if q.Get("client_id") != "turnier" || q.Get("state") == "" ||
		q.Get("redirect_uri") != "http://turnier.test/auth/callback" ||
		q.Get("scope") != "profile" || q.Get("response_type") != "code" {
		t.Fatalf("Auth-URL unvollständig: %s", authURL.String())
	}
	if q.Get("code_challenge") != "" || q.Get("nonce") != "" {
		t.Fatal("generisches OAuth2: PKCE/Nonce dürfen nicht gesendet werden")
	}

	providerAuthorize(t, authURL)
	session := finishLogin(t, mux, authURL, sc)
	if !fp.tokenSeen {
		t.Fatal("Profile-Endpunkt wurde nicht mit dem Access-Token abgefragt")
	}

	// Nutzer + Session in der DB: Provider=Base-URL, sub=E-Mail, Rolle Scorer.
	u, err := st.GetLoginUserBySession(session.Value)
	if err != nil {
		t.Fatalf("Session nach Callback nicht gefunden: %v", err)
	}
	if u.Email != email || u.Name != name || u.Role != model.RoleScorer {
		t.Fatalf("User-Abweichung: %+v", u)
	}
	if u.Provider != fp.srv.URL || u.ProviderSub != email {
		t.Fatalf("Provider/Sub: %q/%q, wollte %q/%q",
			u.Provider, u.ProviderSub, fp.srv.URL, email)
	}

	// Mit der Session: /score wird durchgelassen, /admin 403.
	req := httptest.NewRequest("POST", "/score",
		strings.NewReader("participant_id=1&judge=1&value=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /score nach Login: Status %d, wollte 404 (Guard durch)", w.Code)
	}
	req = httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /admin als Scorer: Status %d, wollte 403", w.Code)
	}
}

func TestOAuth2CallbackNotAllowed(t *testing.T) {
	fp := newFakeOAuth2(t, "turnier", "http://turnier.test/auth/callback",
		"stranger@x.de", "Stranger")
	app, st := newTestApp(t, AuthConfig{OAuth2BaseURL: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, "judge@x.de") // stranger ist nicht dabei
	mux := app.Handler()

	authURL, sc := startLogin(t, mux, "/")
	providerAuthorize(t, authURL)
	req := httptest.NewRequest("GET",
		"/auth/callback?code=fakecode&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Callback mit nicht-freigeschalteter Adresse: Status %d, wollte 403", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("für nicht-freigeschaltete Adresse darf keine Session angesetzt werden")
		}
	}
	if users, err := st.ListLoginUsers(); err != nil || len(users) != 0 {
		t.Fatalf("für abgewiesene Adresse darf kein User angelegt werden (n=%d, err=%v)",
			len(users), err)
	}
}

func TestOAuth2CallbackWrongState(t *testing.T) {
	fp := newFakeOAuth2(t, "turnier", "http://turnier.test/auth/callback",
		"judge@x.de", "J.")
	app, st := newTestApp(t, AuthConfig{OAuth2BaseURL: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	mux := app.Handler()

	_, sc := startLogin(t, mux, "/")
	req := httptest.NewRequest("GET", "/auth/callback?code=fakecode&state=gefake", nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Callback mit falschem State: Status %d, wollte 400", w.Code)
	}
}

func TestOAuth2ProviderOffline(t *testing.T) {
	// Authorize-Endpunkt unerreichbar (geschlossener Port) → Login-Start
	// mit 503, App läuft weiter.
	app, _ := newTestApp(t, AuthConfig{OAuth2BaseURL: "http://127.0.0.1:1"})
	mux := app.Handler()

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Login mit unerreichtem Provider: Status %d, wollte 503", w.Code)
	}
	req = httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Health-Check: Status %d, wollte 200", w.Code)
	}
}

func TestOAuth2ProfileWithoutEmail(t *testing.T) {
	// Profile ohne email/username/identifier → Login muss mit 502 scheitern.
	fp := newFakeOAuth2(t, "turnier", "http://turnier.test/auth/callback",
		"judge@x.de", "J.")
	app, st := newTestApp(t, AuthConfig{OAuth2BaseURL: fp.srv.URL})
	giveRole(t, st, model.RoleScorer, "judge@x.de")
	mux := app.Handler()

	authURL, sc := startLogin(t, mux, "/")
	providerAuthorize(t, authURL)
	fp.withEmail = false
	req := httptest.NewRequest("GET",
		"/auth/callback?code=fakecode&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("Callback ohne E-Mail im Profile: Status %d, wollte 502", w.Code)
	}
}

// ---------- Rollen-Verwaltung (Admin-UI) ----------

func TestAdminRoleManagement(t *testing.T) {
	// boss ist per DB-Eintrag admin (das steht für den -admins-Seed beim
	// Start bzw. dem Hinzufügen in der UI).
	app, st := newTestApp(t, AuthConfig{})
	giveRole(t, st, model.RoleAdmin, "boss@x.de")
	mux := app.Handler()

	// Admin-Session (Flag-Eintrag).
	uid, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "b", Email: "boss@x.de", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok-boss", uid, time.Hour); err != nil {
		t.Fatal(err)
	}

	post := func(path string, form url.Values, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	get := func(path, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// 1. Admin-Seite zeigt den Berechtigungen-Bereich inkl. Seed-Eintrag.
	w := get("/admin", "tok-boss")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /admin: Status %d, wollte 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "Berechtigungen") ||
		!strings.Contains(body, "boss@x.de") {
		t.Fatal("Berechtigungen-Bereich bzw. Seed-Eintrag fehlt auf der Admin-Seite")
	}

	// 2. Scorer hinzufügen (Groß/Kleinreibung normalisiert) → wirkt sofort.
	w = post("/admin/role/add",
		url.Values{"email": {"Judge2@X.de"}, "role": {model.RoleScorer}}, "tok-boss")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "wirken sofort") {
		t.Fatalf("Rolle hinzufügen: Status %d / keine Erfolgsmeldung", w.Code)
	}
	if got := app.auth.RoleFor("judge2@x.de"); got != model.RoleScorer {
		t.Fatalf("RoleFor nach Hinzufügen: %q, wollte bewerten", got)
	}
	uidJ2, _ := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "j2", Email: "judge2@x.de", Role: model.RoleScorer})
	_ = st.CreateLoginSession("tok-j2", uidJ2, time.Hour)
	if w = post("/score",
		url.Values{"participant_id": {"1"}, "judge": {"1"}, "value": {"50"}}, "tok-j2"); w.Code != http.StatusNotFound {
		t.Fatalf("POST /score als neu hinzugefügter Scorer: Status %d, wollte 404 (Guard durch)", w.Code)
	}

	// 3. Duplikat → Fehlermeldung.
	w = post("/admin/role/add",
		url.Values{"email": {"judge2@x.de"}, "role": {model.RoleScorer}}, "tok-boss")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "bereits") {
		t.Fatal("Duplikat-Hinzufügen sollte Fehlermeldung liefern")
	}

	// 4. Admin hinzufügen → darf /admin öffnen.
	if w = post("/admin/role/add",
		url.Values{"email": {"boss2@x.de"}, "role": {model.RoleAdmin}}, "tok-boss"); w.Code != http.StatusOK {
		t.Fatalf("Admin hinzufügen: Status %d", w.Code)
	}
	uidB2, _ := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "b2", Email: "boss2@x.de", Role: model.RoleAdmin})
	_ = st.CreateLoginSession("tok-b2", uidB2, time.Hour)
	if w = get("/admin", "tok-b2"); w.Code != http.StatusOK {
		t.Fatalf("GET /admin als neu hinzugefügter Admin: Status %d, wollte 200", w.Code)
	}

	// 5. Entfernen → Rolle entzogen, Session verworfen (303 zum Login).
	if w = post("/admin/role/remove",
		url.Values{"email": {"judge2@x.de"}, "role": {model.RoleScorer}}, "tok-boss"); w.Code != http.StatusOK {
		t.Fatalf("Rolle entfernen: Status %d", w.Code)
	}
	if got := app.auth.RoleFor("judge2@x.de"); got != "" {
		t.Fatalf("RoleFor nach Entfernen: %q, wollte keine Rolle", got)
	}
	if w = post("/score",
		url.Values{"participant_id": {"1"}, "judge": {"1"}, "value": {"50"}}, "tok-j2"); w.Code != http.StatusSeeOther {
		t.Fatalf("POST /score nach Entzug: Status %d, wollte 303 (Login-Redirect)", w.Code)
	}

	// 6. Ungültige Eingaben → Fehler.
	if w = post("/admin/role/add",
		url.Values{"email": {"judge3@x.de"}, "role": {"superuser"}}, "tok-boss"); !strings.Contains(w.Body.String(), "unbekannte Rolle") {
		t.Fatal("ungültige Rolle sollte Fehlermeldung liefern")
	}
	if w = post("/admin/role/add",
		url.Values{"email": {"keine-adresse"}, "role": {model.RoleScorer}}, "tok-boss"); !strings.Contains(w.Body.String(), "E-Mail") {
		t.Fatal("ungültige E-Mail sollte Fehlermeldung liefern")
	}

	// 7. Scorer darf die Berechtigungen nicht verwalten (Guard → 303).
	uidJ3, _ := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "j3", Email: "judge3@x.de", Role: model.RoleScorer})
	_ = st.CreateLoginSession("tok-j3", uidJ3, time.Hour)
	w = post("/admin/role/add",
		url.Values{"email": {"x@x.de"}, "role": {model.RoleScorer}}, "tok-j3")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/role/add als Scorer: Status %d, wollte 303 (Login-Redirect)", w.Code)
	}

	// 8. Auch Seed-Einträge (-admins) sind normale DB-Einträge: sie lassen
	//    sich entfernen (hier via dem zweiten Admin, boss2).
	w = post("/admin/role/remove",
		url.Values{"email": {"boss@x.de"}, "role": {model.RoleAdmin}}, "tok-b2")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "entfernt") {
		t.Fatalf("Seed-Admin entfernen: Status %d / keine Erfolgsmeldung", w.Code)
	}
	if got := app.auth.RoleFor("boss@x.de"); got != "" {
		t.Fatalf("RoleFor nach Entfernen des Seed-Admins: %q, wollte keine Rolle", got)
	}
}
