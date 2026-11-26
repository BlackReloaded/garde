// auth.go: Login (OIDC oder generisches OAuth2, z. B. mailcow),
// Server-Sessions, Rollen-Middleware und optional ein allgemeines
// Zugangs-Passwort.
//
// Provider-Modi (höchstens einer konfiguriert):
//
//   - OIDC: Authorization Code + PKCE, Discovery, ID-Token-Verifizierung.
//   - Generisches OAuth2 (z. B. der eingebaute Provider von mailcow):
//     feste Endpoints (/oauth/authorize, /oauth/token, /oauth/profile),
//     Access-Token statt ID-Token, Benutzerdaten über das Profile-Endpunkt.
//   - Kein Provider: nur Passwörter (-password und/oder -score-password) –
//     die App startet dann ohne Login.
//
// Die Passwörter sind Zugangs-Türen, KEINE Logins (Passwort-Seite /access,
// signiertes Cookie, pro IP nach 5 Fehlversuchen 60 s Pause): -password
// öffnet Live-Ansicht und Exporte für alle, die es kennen; -score-password
// erlaubt nach Eingabe zusätzlich die Punkteeingabe. Auf der Zugangsseite
// sitzt daneben der Login-Button des Providers (falls konfiguriert) –
// OAuth geht also immer direkt auf der ersten Seite. Bewerten per Login
// braucht weiterhin den Rollen-Eintrag in der DB; Admin geht nur per Login.
// Das Bewertungs-Passwort ist ein geteilter Zugang: Die Richter-Nummer
// kommt aus dem Formular, die Identität bleibt unbekannt.
//
// Rollen werden über die Datenbank-Tabelle login_role vergeben (einzige
// Quelle): Einträge legt der Admin in der Admin-UI an; beim Start werden
// optional die Adressen aus -admins dort hinein kopiert. Die Rolle wird
// bei JEDEM Request gegen die aktuelle Liste geprüft: Wird eine Adresse
// entfernt, verliert die Session sofort ihre Rechte (bis zum Logout).
//
// Im OIDC-Modus wird die Discovery erst beim ersten Login-Versuch abgefragt
// (lazy): Die App startet und die Live-Ansicht funktioniert auch, wenn der
// Provider (z. B. wegen fehlendem Internet auf dem Messe-PC) nicht
// erreichbar ist – nur das Einloggen selbst scheitert dann.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"turnier/internal/model"
	"turnier/internal/store"
)

const (
	sessionCookie = "turnier_session"
	stateCookie   = "turnier_auth_state"
	accessCookie  = "turnier_access" // Nachweis des Zugangs-Passworts
	stateTTL      = 5 * time.Minute
)

// AuthConfig ist die Login-Konfiguration (aus Flags/Env, s. main.go).
// Ohne Provider (Issuer/OAuth2BaseURL) muss mindestens eines der beiden
// Passwörter gesetzt sein – sonst startet die App ohne jede Auth, und das
// ist nicht gedacht.
type AuthConfig struct {
	Issuer string // OIDC: Discovery-URL des Providers

	OAuth2BaseURL      string // OAuth2: z. B. https://mail.kohlbau.de
	OAuth2AuthorizeURL string // Optionale Overrides (Default: <Base>/oauth/…)
	OAuth2TokenURL     string
	OAuth2ProfileURL   string
	OAuth2Scope        string // Default "profile"

	ClientID     string
	ClientSecret string
	RedirectURL  string        // muss exakt im Provider registriert sein
	SessionTTL   time.Duration // Laufzeit der Session (Default 12 h)

	Password string // Allgemeines Zugangs-Passwort (nur Lese-Zugang, s. oben)

	ScorePassword string // Bewertungs-Passwort (öffnet zusätzlich die Noteneingabe)
}

// Auth bündelt Provider-Client (OIDC oder generisches OAuth2),
// Rollen-Liste (DB), Session-Handling und das Zugangs-Passwort.
type Auth struct {
	store        *store.Store
	generic      bool   // generischer OAuth2-Modus (z. B. mailcow)
	hasProvider  bool   // OIDC oder OAuth2 konfiguriert (sonst nur Passwort)
	providerName string // Wert für login_user.provider (Issuer bzw. Base-URL)
	authURL      string // generisch: Authorize-Endpunkt
	profileURL   string // generisch: Profile-Endpunkt
	clientID     string
	clientSecret string
	redirectURL  string
	secure       bool // Secure-Cookie, wenn der Provider per HTTPS aufgerufen wird
	sessionTTL   time.Duration
	signingKey   []byte // nur im Speicher: signiert State- und Zugangs-Cookies
	conf         *oauth2.Config

	// Zugangs-Passwörter (-password, -score-password): wenn gesetzt,
	// brauchen anonyme Requests ein gültiges access-Cookie für die
	// Lese-Routen; das Bewertungs-Passwort-Cookie öffnet zusätzlich die
	// Noteneingabe. loginPath ist das Ziel, wenn nach Login verlangt wird
	// (ohne Provider führt es zum Passwort-Formular), scoreLoginPath das
	// Ziel für Bewerten-Requests. pwMu/pwAttempts zählt Fehlversuche pro
	// IP für die Passwort-Eingabe.
	accessPass []byte
	scorePass  []byte
	loginPath  string

	pwMu       sync.Mutex
	pwAttempts map[string]*pwCounter

	// Rollen-Einträge (DB-Tabelle login_role, die einzige Quelle) als
	// Cache: einmal geladen, bei jeder Änderung neu geladen → wirksam ab
	// dem nächsten Request, ohne Neustart.
	roleMu       sync.Mutex
	dbRoles      map[string]map[string]bool // Rolle → E-Mail-Menge
	dbRolesReady bool

	mu       sync.Mutex
	oidcProv *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// NewAuth erzeugt den Auth-Handler. Es wird KEIN Netzwerk-Kontakt
// aufgenommen (OIDC-Discovery ist lazy) – Tests können frei damit arbeiten.
func NewAuth(st *store.Store, cfg AuthConfig) (*Auth, error) {
	providerSet := cfg.Issuer != "" || cfg.OAuth2BaseURL != ""
	if cfg.Issuer != "" && cfg.OAuth2BaseURL != "" {
		return nil, errors.New("Login: nur EIN Provider – Issuer (OIDC) und " +
			"OAuth2BaseURL nicht gleichzeitig")
	}
	if !providerSet && cfg.Password == "" && cfg.ScorePassword == "" {
		return nil, errors.New("Login: ohne Provider ist mindestens ein " +
			"Passwort Pflicht (Password und/oder ScorePassword) – sonst " +
			"startet die App ohne Auth")
	}
	if providerSet && (cfg.ClientID == "" || cfg.RedirectURL == "") {
		return nil, errors.New("Login: ClientID und RedirectURL sind Pflicht")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	a := &Auth{
		store:        st,
		generic:      cfg.OAuth2BaseURL != "",
		hasProvider:  providerSet,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		secure:       strings.HasPrefix(strings.ToLower(cfg.RedirectURL), "https://"),
		sessionTTL:   cfg.SessionTTL,
		pwAttempts:   map[string]*pwCounter{},
	}
	if cfg.Password != "" {
		a.accessPass = []byte(cfg.Password)
	}
	if cfg.ScorePassword != "" {
		a.scorePass = []byte(cfg.ScorePassword)
	}
	a.loginPath = "/auth/login"
	if !providerSet {
		a.loginPath = "/access"
	}
	// Provider-Client nur mit Provider bauen: ohne Provider (nur
	// Zugangs-Passwort) gibt es conf = nil – die Login-Routen laufen
	// dann nie hinein (login leitet aufs Passwort-Formular um).
	if providerSet {
		if a.generic {
			base := strings.TrimRight(cfg.OAuth2BaseURL, "/")
			a.providerName = base
			a.authURL = cfg.OAuth2AuthorizeURL
			if a.authURL == "" {
				a.authURL = base + "/oauth/authorize"
			}
			tokenURL := cfg.OAuth2TokenURL
			if tokenURL == "" {
				tokenURL = base + "/oauth/token"
			}
			a.profileURL = cfg.OAuth2ProfileURL
			if a.profileURL == "" {
				a.profileURL = base + "/oauth/profile"
			}
			scope := strings.TrimSpace(cfg.OAuth2Scope)
			if scope == "" {
				scope = "profile"
			}
			a.conf = &oauth2.Config{
				ClientID:     cfg.ClientID,
				ClientSecret: cfg.ClientSecret,
				RedirectURL:  cfg.RedirectURL,
				Scopes:       []string{scope},
				Endpoint: oauth2.Endpoint{
					AuthURL:  a.authURL,
					TokenURL: tokenURL,
				},
			}
		} else {
			a.providerName = cfg.Issuer
			a.conf = &oauth2.Config{
				ClientID:     cfg.ClientID,
				ClientSecret: cfg.ClientSecret,
				RedirectURL:  cfg.RedirectURL,
				Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
			}
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	a.signingKey = key
	return a, nil
}

// RoleFor liefert die Rolle einer E-Mail-Adresse ("" = keine
// Berechtigung). Quelle ist die DB-Tabelle login_role (Admin-UI bzw.
// beim Start per -admins gefüllt). Admins gewinnen vor Scorern.
func (a *Auth) RoleFor(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if a.roleHas(model.RoleAdmin, email) {
		return model.RoleAdmin
	}
	if a.roleHas(model.RoleScorer, email) {
		return model.RoleScorer
	}
	return ""
}

// roleHas prüft die (gecachte) DB-Liste.
func (a *Auth) roleHas(role, email string) bool {
	a.roleMu.Lock()
	defer a.roleMu.Unlock()
	a.ensureDBRolesLocked()
	return a.dbRoles[role][email]
}

// ensureDBRolesLocked lädt die UI-verwalteten Rollen-Einträge aus der DB
// (einmalig). Wird NUR mit gelocktem roleMu aufgerufen.
func (a *Auth) ensureDBRolesLocked() {
	if a.dbRolesReady {
		return
	}
	a.dbRoles = loadRoleMaps(a.store)
	a.dbRolesReady = true
}

// refreshDBRoles liest die DB-Einträge nach einer Änderung neu.
func (a *Auth) refreshDBRoles() {
	a.roleMu.Lock()
	defer a.roleMu.Unlock()
	a.dbRoles = loadRoleMaps(a.store)
	a.dbRolesReady = true
}

func loadRoleMaps(st *store.Store) map[string]map[string]bool {
	m := map[string]map[string]bool{
		model.RoleAdmin:  {},
		model.RoleScorer: {},
	}
	rows, err := st.ListLoginRoles()
	if err != nil {
		log.Printf("Rollen-Einträge laden: %v", err)
		return m
	}
	for _, r := range rows {
		if m[r.Role] == nil {
			m[r.Role] = map[string]bool{}
		}
		m[r.Role][r.Email] = true
	}
	return m
}

// ---------- Rollen-Verwaltung (Admin-UI) ----------

// RoleLabel ist die Anzeigename einer Rolle.
func RoleLabel(role string) string {
	switch role {
	case model.RoleAdmin:
		return "admin"
	default:
		return "bewerten"
	}
}

// ListRoles liefert die Rollen-Liste (DB-Tabelle login_role) für die
// Admin-UI. Eine Adresse kann in beiden Rollen auftauchen; sie erscheint
// dann als Einträge in beiden Listen.
func (a *Auth) ListRoles() []model.RoleEntry {
	a.roleMu.Lock()
	a.ensureDBRolesLocked()
	db := a.dbRoles
	a.roleMu.Unlock()

	out := []model.RoleEntry{}
	for role, set := range db {
		for e := range set {
			out = append(out, model.RoleEntry{Role: role, Email: e})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role == model.RoleAdmin
		}
		return out[i].Email < out[j].Email
	})
	return out
}

// validEmail ist ein leichter Plausibilitätscheck (genau ein @, beide
// Seiten gefüllt). Endgültig gilt, was der Login-Provider akzeptiert.
func validEmail(e string) bool {
	if len(e) == 0 || len(e) > 254 || strings.Count(e, "@") != 1 {
		return false
	}
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-1
}

// AddRole ergänzt eine Adresse per UI (DB-Eintrag, sofort wirksam).
func (a *Auth) AddRole(email, role string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if role != model.RoleAdmin && role != model.RoleScorer {
		return errors.New("unbekannte Rolle (nur admin oder bewerten)")
	}
	if !validEmail(email) {
		return errors.New("bitte eine gültige E-Mail-Adresse angeben")
	}
	if cur := a.RoleFor(email); cur != "" {
		if cur == role {
			return fmt.Errorf("%s ist bereits als %s eingetragen", email, RoleLabel(role))
		}
		if cur == model.RoleAdmin {
			return fmt.Errorf("%s hat bereits höhere Rechte (admin)", email)
		}
	}
	if err := a.store.AddLoginRole(role, email); err != nil {
		return err
	}
	a.refreshDBRoles()
	log.Printf("Rollen-Liste: %s hinzugefügt als %s (via Admin-UI)", email, role)
	return nil
}

// RemoveRole entfernt einen Eintrag aus der Rollen-Liste (sofort
// wirksam, auch für per -admins gesetzte Einträge).
func (a *Auth) RemoveRole(email, role string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if role != model.RoleAdmin && role != model.RoleScorer {
		return errors.New("unbekannte Rolle (nur admin oder bewerten)")
	}
	if err := a.store.RemoveLoginRole(role, email); err != nil {
		return err
	}
	a.refreshDBRoles()
	log.Printf("Rollen-Liste: %s entfernt aus %s (via Admin-UI)", email, role)
	return nil
}

// provider liefert OIDC-Provider und Token-Verifier. Die Discovery wird
// pro Aufruf bis zum Erfolg wiederholt (kein Fehler-Cache): Wird der
// Provider später wieder erreichbar, funktioniert der Login ohne Neustart.
func (a *Auth) provider() (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.oidcProv != nil {
		return a.oidcProv, a.verifier, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, a.providerName)
	if err != nil {
		return nil, nil, fmt.Errorf("OIDC-Discovery unter %s fehlgeschlagen: %w", a.providerName, err)
	}
	a.conf.Endpoint = p.Endpoint()
	a.oidcProv = p
	a.verifier = p.Verifier(&oidc.Config{ClientID: a.clientID})
	return a.oidcProv, a.verifier, nil
}

// ---------- State (signiertes Cookie) ----------

// statePayload ist der Inhalt des kurzen State-Cookies zwischen Login-Start
// und Callback: state + nonce + PKCE-Verifier + Ziel-URL.
type statePayload struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x,omitempty"`
	Exp      int64  `json:"e"`
}

func (a *Auth) signPayload(p statePayload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write(data)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(data) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

func (a *Auth) verifyPayload(v string) (*statePayload, error) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("ungültiges Format")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("ungültige Kodierung")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("ungültige Signatur-Kodierung")
	}
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("Signatur passt nicht")
	}
	var p statePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if time.Now().Unix() > p.Exp {
		return nil, errors.New("Anfrage abgelaufen")
	}
	return &p, nil
}

func randomURL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sanitizeNext erlaubt nur relative Pfade (keine "//host" oder http://…).
func sanitizeNext(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	return v
}

// ---------- Anmeldezustand für Templates ----------

// AuthState ist der Anmeldezustand, den die Templates sehen.
type AuthState struct {
	LoggedIn bool
	Email    string
	Name     string
	Role     string // effektive Rolle (aktueller DB-Stand der Rollen-Liste)
	CanScore bool   // bewerten oder admin – oder gültiges Bewertungs-Passwort
	CanAdmin bool
	LoginURL string // Ziel des „anmelden"-Links im Lese-Panel (s. authState)
}

// authState löst die Session des Requests auf. "Eingeloggt" ohne Rolle
// (Adresse nicht mehr in der Rollen-Liste) wird mit Rolle "" behandelt –
// die Middleware leitet dann zum Login um, der im Callback abweist.
// Das Bewertungs-Passwort ist kein Login (kein LoggedIn), aber es schaltet
// CanScore frei. LoginURL zeigt dorthin, wo man Bewerten-Rechte bekommt:
// Zugangsseite, wenn ein Bewertungs-Passwort existiert (dort sitzt auch
// der Provider-Button), sonst der normale Login.
func (a *Auth) authState(r *http.Request) AuthState {
	as := AuthState{LoginURL: a.scoreLoginPath()}
	if a.scoreCookieOK(r) {
		as.CanScore = true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return as
	}
	u, err := a.store.GetLoginUserBySession(c.Value)
	if err != nil {
		return as
	}
	role := a.RoleFor(u.Email)
	as.LoggedIn = true
	as.Email = u.Email
	as.Name = u.Name
	as.Role = role
	if role == model.RoleAdmin || role == model.RoleScorer {
		as.CanScore = true
		as.CanAdmin = role == model.RoleAdmin
	}
	return as
}

// ---------- Routen ----------

// login startet den Login-Flow (OIDC oder generisches OAuth2; Rolle: keine –
// die Entscheidung fällt im Callback). next= leitet die Ziel-URL mit.
func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	if as := a.authState(r); as.LoggedIn && (as.CanScore || as.CanAdmin) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	if !a.hasProvider {
		// Kein Provider konfiguriert: /auth/login hat hier nichts zu
		// suchen (kein Discovery-Kontakt) – weiter zum Zugangs-Formular.
		http.Redirect(w, r, "/access?next="+url.QueryEscape(next),
			http.StatusSeeOther)
		return
	}
	if a.generic {
		// Kein Discovery-Dokument im generischen Modus: Erreichbarkeit wird
		// per HTTP-Antwort auf den Authorize-Endpunkt geprüft.
		if err := a.genericReachable(); err != nil {
			a.errorPage(w, http.StatusServiceUnavailable, "Anbieter nicht erreichbar",
				"Der Login-Anbieter antwortet gerade nicht (offline?). Bereits "+
					"eingeloggte Sessions funktionieren weiter.")
			return
		}
	} else {
		if _, _, err := a.provider(); err != nil {
			a.errorPage(w, http.StatusServiceUnavailable, "Anbieter nicht erreichbar",
				"Der Login-Anbieter antwortet gerade nicht (offline?). Bereits "+
					"eingeloggte Sessions funktionieren weiter.")
			return
		}
	}
	state, err := randomURL(24)
	if err != nil {
		a.serverError(w, err)
		return
	}
	payload := statePayload{
		State: state,
		Next:  next,
		Exp:   time.Now().Add(stateTTL).Unix(),
	}
	var authURL string
	if a.generic {
		// Kein PKCE, kein Nonce: Das generische OAuth2 (z. B. mailcow) kennt
		// beides nicht; der Client ist konfidenter (Client-Secret).
		authURL = a.conf.AuthCodeURL(state)
	} else {
		payload.Nonce, err = randomURL(24)
		if err != nil {
			a.serverError(w, err)
			return
		}
		payload.Verifier, err = randomURL(32)
		if err != nil {
			a.serverError(w, err)
			return
		}
		authURL = a.conf.AuthCodeURL(state,
			oidc.Nonce(payload.Nonce),
			oauth2.S256ChallengeOption(payload.Verifier))
	}
	v, err := a.signPayload(payload)
	if err != nil {
		a.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: v,
		Path: "/", MaxAge: int(stateTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure,
	})
	log.Printf("Login-Start (next=%s)", next)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// genericReachable prüft, ob der generische OAuth2-Provider antwortet
// (irgendeine HTTP-Antwort reicht – z. B. der Redirect zur Provider-Login).
func (a *Auth) genericReachable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", a.authURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// callback nimmt den OIDC-Flow entgegen: State/Nonce prüfen, Code tauschen,
// ID-Token verifizieren, Rolle aus der DB-Liste bestimmen, Session anlegen.
func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.errorPage(w, http.StatusBadRequest, "Login fehlgeschlagen", "Ungültige Anfrage.")
		return
	}
	if errName := r.Form.Get("error"); errName != "" {
		a.clearStateCookie(w)
		msg := errName
		if d := r.Form.Get("error_description"); d != "" {
			msg += " (" + d + ")"
		}
		a.errorPage(w, http.StatusBadRequest, "Login fehlgeschlagen",
			"Der Anbieter hat den Login abgelehnt: "+msg)
		return
	}
	code := r.Form.Get("code")
	state := r.Form.Get("state")
	sc, err := r.Cookie(stateCookie)
	if err != nil || code == "" || state == "" {
		a.clearStateCookie(w)
		a.errorPage(w, http.StatusBadRequest, "Login fehlgeschlagen",
			"Ungültige oder abgelaufene Login-Anfrage. Bitte erneut anmelden.")
		return
	}
	payload, err := a.verifyPayload(sc.Value)
	if err != nil {
		a.clearStateCookie(w)
		log.Printf("Callback: State-Cookie ungültig: %v", err)
		a.errorPage(w, http.StatusBadRequest, "Login fehlgeschlagen",
			"State-Prüfung fehlgeschlagen. Bitte erneut anmelden.")
		return
	}
	if payload.State != state {
		a.clearStateCookie(w)
		log.Printf("Callback: State-Parameter passt nicht zum Cookie (CSRF-Schutz)")
		a.errorPage(w, http.StatusBadRequest, "Login fehlgeschlagen",
			"State-Prüfung fehlgeschlagen. Bitte erneut anmelden.")
		return
	}
	id, perr := a.exchangeIdentity(r.Context(), code, payload)
	if perr != nil {
		a.errorPage(w, perr.status, perr.title, perr.msg)
		return
	}
	role := a.RoleFor(id.email)
	if role == "" {
		a.clearStateCookie(w)
		log.Printf("Login abgelehnt: %s ist nicht freigeschaltet", id.email)
		a.errorPage(w, http.StatusForbidden, "Keine Berechtigung",
			fmt.Sprintf("Die Adresse %s ist nicht für die Datenpflege freigeschaltet "+
				"(Admin/Bewertung).", template.HTMLEscapeString(id.email)))
		return
	}
	userID, err := a.store.UpsertLoginUser(model.LoginUser{
		Provider:    a.providerName,
		ProviderSub: id.sub,
		Email:       id.email,
		Name:        id.name,
		Role:        role,
	})
	if err != nil {
		a.serverError(w, err)
		return
	}
	sessToken, err := randomURL(32)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if err := a.store.CreateLoginSession(sessToken, userID, a.sessionTTL); err != nil {
		a.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessToken,
		Path: "/", MaxAge: int(a.sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure,
	})
	a.clearStateCookie(w)
	log.Printf("Angemeldet: %s (%s, Rolle %s)", id.email, id.sub, role)
	http.Redirect(w, r, sanitizeNext(payload.Next), http.StatusSeeOther)
}

// identity ist die aus dem Login-Flow extrahierte Identität.
type identity struct {
	email string // normalisiert (klein, getrimmt)
	name  string
	sub   string // stabile Provider-Id (ID-Token-Sub bzw. E-Mail)
}

// providerError ist ein nutzerpräsentierbarer Fehler des Provider-Flows.
type providerError struct {
	status int
	title  string
	msg    string
}

// exchangeIdentity tauscht den Code und extrahiert die Identität – je nach
// Modus über das verifizierte ID-Token (OIDC) oder das Profile-Endpunkt
// (generisches OAuth2, z. B. mailcow).
func (a *Auth) exchangeIdentity(ctx context.Context, code string, payload *statePayload) (identity, *providerError) {
	if a.generic {
		return a.genericIdentity(ctx, code)
	}
	return a.oidcIdentity(ctx, code, payload)
}

// oidcIdentity: Code tauschen, ID-Token verifizieren (inkl. Nonce),
// E-Mail/Name aus den Claims (notfalls User-Info).
func (a *Auth) oidcIdentity(ctx context.Context, code string, payload *statePayload) (identity, *providerError) {
	provider, verifier, err := a.provider()
	if err != nil {
		return identity{}, &providerError{http.StatusServiceUnavailable, "Anbieter nicht erreichbar", err.Error()}
	}
	token, err := a.conf.Exchange(ctx, code, oauth2.VerifierOption(payload.Verifier))
	if err != nil {
		log.Printf("Callback: Token-Austausch fehlgeschlagen: %v", err)
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Code-Tausch mit dem Anbieter ist fehlgeschlagen."}
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Anbieter hat kein ID-Token zurückgegeben."}
	}
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		log.Printf("Callback: ID-Token ungültig: %v", err)
		return identity{}, &providerError{http.StatusUnauthorized, "Login fehlgeschlagen",
			"Das ID-Token des Anbieters ist ungültig."}
	}
	// Nonce-Prüfung ist bei go-oidc v3.21+ Verantwortung des Callers.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(payload.Nonce)) != 1 {
		log.Printf("Callback: Nonce passt nicht (Token-Reuse?)")
		return identity{}, &providerError{http.StatusUnauthorized, "Login fehlgeschlagen",
			"Nonce-Prüfung fehlgeschlagen (Token-Reuse?). Bitte erneut anmelden."}
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	idToken.Claims(&claims)
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		// Manche Provider liefern die E-Mail nur am User-Info-Endpunkt.
		ui, err := provider.UserInfo(ctx, a.conf.TokenSource(ctx, token))
		if err != nil {
			return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
				"Keine E-Mail-Adresse erhalten (ID-Token und User-Info)."}
		}
		ui.Claims(&claims)
		email = strings.ToLower(strings.TrimSpace(claims.Email))
	}
	if email == "" {
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Anbieter liefert keine E-Mail-Adresse – die Anmeldung ist nicht möglich."}
	}
	return identity{email: email, name: claims.Name, sub: idToken.Subject}, nil
}

// genericIdentity: Code tauschen (z. B. mailcow) und die Benutzerdaten über
// das Profile-Endpunkt mit dem Access-Token laden.
func (a *Auth) genericIdentity(ctx context.Context, code string) (identity, *providerError) {
	token, err := a.conf.Exchange(ctx, code)
	if err != nil {
		log.Printf("Callback: Token-Austausch fehlgeschlagen: %v", err)
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Code-Tausch mit dem Anbieter ist fehlgeschlagen."}
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, "GET", a.profileURL, nil)
	if err != nil {
		return identity{}, &providerError{http.StatusInternalServerError, "Interner Fehler",
			"Profile-Anfrage konnte nicht aufgebaut werden."}
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Callback: Profile-Request fehlgeschlagen: %v", err)
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Die Benutzerdaten konnten nicht vom Anbieter geladen werden."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("Callback: Profile-Endpunkt antwortete mit Status %d", resp.StatusCode)
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Profile-Endpunkt des Anbieters hat einen Fehler gemeldet."}
	}
	// Das Format folgt dem mailcow-Profile-Endpunkt; Username/Identifier
	// dienen als Fallback, falls kein explizites "email"-Feld kommt.
	var profile struct {
		Email       string `json:"email"`
		Username    string `json:"username"`
		Identifier  string `json:"identifier"`
		FullName    string `json:"full_name"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&profile); err != nil {
		log.Printf("Callback: Profile-Antwort nicht lesbar: %v", err)
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Die Profile-Antwort des Anbieters ist ungültig."}
	}
	email := strings.ToLower(strings.TrimSpace(
		firstNonEmpty(profile.Email, profile.Username, profile.Identifier)))
	if email == "" {
		return identity{}, &providerError{http.StatusBadGateway, "Login fehlgeschlagen",
			"Der Anbieter liefert keine E-Mail-Adresse – die Anmeldung ist nicht möglich."}
	}
	return identity{
		email: email,
		name:  firstNonEmpty(profile.FullName, profile.DisplayName),
		sub:   email, // Mailbox-Name: stabil über Logins hinweg
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// logout beendet die Session (POST, damit kein <img>-CSRF möglich ist).
func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.store.DeleteLoginSession(c.Value); err != nil {
			log.Printf("Logout: Session löschen: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// ---------- Middleware ----------

// scoreLoginPath ist das Ziel für Requests, die Bewerten-Rechte brauchen:
// mit Bewertungs-Passwort die Zugangsseite (dort gibt es das Passwortfeld
// und – bei Provider – den Login-Button), sonst der normale Login.
func (a *Auth) scoreLoginPath() string {
	if a.scorePass != nil {
		return "/access"
	}
	return a.loginPath
}

// scoreCookieOK prüft auf ein gültiges Zugangs-Cookie, das mit dem
// Bewertungs-Passwort geholt wurde (accessPayload.Score).
func (a *Auth) scoreCookieOK(r *http.Request) bool {
	if a.scorePass == nil {
		return false
	}
	c, err := r.Cookie(accessCookie)
	if err != nil || c.Value == "" {
		return false
	}
	p, ok := a.validAccess(c.Value)
	return ok && p.Score
}

// requireScore lässt Bewerten-Requests durch: Provider-Session mit Rolle
// bewerten/admin ODER gültiges Bewertungs-Passwort-Cookie (geteilter
// Zugang – die Richter-Nummer kommt aus dem Formular). Ohne beides führt
// der Weg aufs Login-Ziel, wo beides angeboten wird.
func (a *Auth) requireScore(h http.HandlerFunc) http.HandlerFunc {
	guard := a.requireRoleAt(a.scoreLoginPath(), model.RoleScorer, h)
	return func(w http.ResponseWriter, r *http.Request) {
		if a.scoreCookieOK(r) {
			h.ServeHTTP(w, r)
			return
		}
		guard(w, r)
	}
}

// redirectLogin schickt den Request zum Login: HTMX-Requests bekommen
// HX-Redirect (Page-Navigation, z. B. bei abgelaufener Session mitten im
// Turnier), normale Requests einen 303-Redirect.
func redirectLogin(w http.ResponseWriter, r *http.Request, login, next string) {
	loc := login + "?next=" + url.QueryEscape(next)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", loc)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// requireRole lässt den Handler nur durch, wenn der Request angemeldet ist
// und mindestens minRole hat. Nicht angemeldet → Redirect zum Login
// (Browser und HTMX folgen dem); angemeldet, aber zu wenig Rechte → 403.
func (a *Auth) requireRole(minRole string, h http.HandlerFunc) http.HandlerFunc {
	return a.requireRoleAt(a.loginPath, minRole, h)
}

// requireRoleAt ist der Guard mit eigenem Login-Ziel.
func (a *Auth) requireRoleAt(login, minRole string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		as := a.authState(r)
		if !as.LoggedIn {
			next := r.URL.Path
			if q := r.URL.RawQuery; q != "" {
				next += "?" + q
			}
			redirectLogin(w, r, login, next)
			return
		}
		if roleRank(as.Role) < roleRank(minRole) {
			if as.Role == "" {
				// Rolle komplett entzogen (nicht mehr in der Rollen-Liste):
				// Session verwerfen, neu einloggen (Callback weist dann ab).
				_ = a.logoutSessionOnly(w, r)
				redirectLogin(w, r, login, r.URL.Path)
				return
			}
			// Angemeldet, aber zu wenig Rechte: 403, Session bleibt erhalten.
			a.errorPage(w, http.StatusForbidden, "Keine Berechtigung",
				"Ihre Rolle erlaubt diesen Zugriff nicht.")
			return
		}
		h.ServeHTTP(w, r)
	}
}

// logoutSessionOnly entfernt nur die Session (ohne Redirect/Redirect-URL).
func (a *Auth) logoutSessionOnly(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.store.DeleteLoginSession(c.Value); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func roleRank(role string) int {
	switch role {
	case model.RoleAdmin:
		return 2
	case model.RoleScorer:
		return 1
	default:
		return 0
	}
}

// ---------- Zugangs-Passwort (Lese-Zugang) ----------

const (
	pwMaxFails  = 5
	pwBlockTime = 60 * time.Second
)

// pwCounter zählt Passwort-Fehlversuche einer IP innerhalb eines festen
// Fensters (pwBlockTime). Ab pwMaxFails werden weitere Eingaben ignoriert.
type pwCounter struct {
	fails int
	until time.Time // Ende des Fensters (= Ende einer Sperre)
}

// accessOK prüft den Lese-Zugang. Ohne Zugangs-Passwort ist alles offen
// (Verhalten wie ohne dieses Feature). Mit Passwort genügt ein gültiges
// access-Cookie (mit oder ohne Bewerten-Recht) – oder eine Login-Session
// mit Rolle: Scorer/Admin lesen ohne Umweg. "Eingeloggt" ohne Rolle zählt
// nicht (die Middleware wirft solche Sessions ohnehin raus).
func (a *Auth) accessOK(r *http.Request) bool {
	if a.accessPass == nil {
		return true
	}
	if c, err := r.Cookie(accessCookie); err == nil {
		if _, ok := a.validAccess(c.Value); ok {
			return true
		}
	}
	as := a.authState(r)
	return as.LoggedIn && roleRank(as.Role) > 0
}

// requireAccess legt die Lese-Routen (Live-Ansicht, Partial-Refresh,
// Exporte) hinter das Zugangs-Passwort. Ohne Passwort durchsichtig.
// HTMX-Polling bekommt ein HX-Redirect aufs Formular statt ins Leere zu
// pollern; normale Requests werden 303 umgeleitet.
func (a *Auth) requireAccess(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.accessOK(r) {
			h.ServeHTTP(w, r)
			return
		}
		next := r.URL.Path
		if q := r.URL.RawQuery; q != "" {
			next += "?" + q
		}
		loc := "/access?next=" + url.QueryEscape(next)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", loc)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, loc, http.StatusSeeOther)
	}
}

// accessPayload ist der Inhalt des Zugangs-Cookies: Ablaufdatum und ob es
// mit dem Bewertungs-Passwort geholt wurde (Score → Noteneingabe offen).
type accessPayload struct {
	Exp   int64 `json:"e"`
	Score bool  `json:"s,omitempty"`
}

// signAccess baut den Cookie-Wert (Basis64-Nutzdaten + HMAC über dem
// Signing-Key). Ohne das Secret lässt sich kein Zugang erdenken; nach
// einem Neustart sind alle Zugangs-Cookies pauschal ungültig (neuer
// Key), spätestens nach SessionTTL auch vorher.
func (a *Auth) signAccess(exp time.Time, score bool) (string, error) {
	data, err := json.Marshal(accessPayload{Exp: exp.Unix(), Score: score})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// validAccess prüft Signatur und Ablaufdatum eines Zugangs-Cookie-Werts
// und liefert das Payload (für die Bewerten-Frage).
func (a *Auth) validAccess(v string) (accessPayload, bool) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return accessPayload{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return accessPayload{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return accessPayload{}, false
	}
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return accessPayload{}, false
	}
	var p accessPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return accessPayload{}, false
	}
	if time.Now().Unix() >= p.Exp {
		return accessPayload{}, false
	}
	return p, true
}

// pwBlocked liefert die Restzeit einer IP-Sperre (0 = Eingaben erlaubt).
func (a *Auth) pwBlocked(ip string) time.Duration {
	a.pwMu.Lock()
	defer a.pwMu.Unlock()
	c := a.pwAttempts[ip]
	if c == nil {
		return 0
	}
	if c.fails >= pwMaxFails {
		if rest := time.Until(c.until); rest > 0 {
			return rest
		}
	}
	if time.Now().After(c.until) {
		delete(a.pwAttempts, ip) // Fenster vorbei: wieder bei Null zählen
	}
	return 0
}

// pwFail zählt einen Fehlversuch; true = die IP ist damit (weiterhin)
// über dem Limit.
func (a *Auth) pwFail(ip string) bool {
	a.pwMu.Lock()
	defer a.pwMu.Unlock()
	if len(a.pwAttempts) > 4096 {
		a.pwAttempts = map[string]*pwCounter{} // Notbremse gegen Map-Wachstum
	}
	c := a.pwAttempts[ip]
	if c == nil || time.Now().After(c.until) {
		c = &pwCounter{until: time.Now().Add(pwBlockTime)}
		a.pwAttempts[ip] = c
	}
	c.fails++
	return c.fails >= pwMaxFails
}

// pwReset vergisst Zähler und Sperre einer IP (nach erfolgreichem Login).
func (a *Auth) pwReset(ip string) {
	a.pwMu.Lock()
	defer a.pwMu.Unlock()
	delete(a.pwAttempts, ip)
}

// clientIP ist die Gegenstelle für die Fehlversuchs-Zählung.
// X-Forwarded-For wird bewusst nicht geglaubt: Die App läuft ohne
// Reverse Proxy (TLS-Terminierung in der App), RemoteAddr ist der Client.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// accessForm zeigt das Eingabefeld (GET /access). Ohne konfiguriertes
// Passwort gibt es hier nichts – zurück zur (offenen) Live-Ansicht.
func (a *Auth) accessForm(w http.ResponseWriter, r *http.Request) {
	if a.accessPass == nil && a.scorePass == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.accessPage(w, http.StatusOK, "", sanitizeNext(r.URL.Query().Get("next")))
}

// accessSubmit prüft das eingegebene Passwort gegen beide Zugangs-
// Passwörter: Treffer → signiertes Zugangs-Cookie (beim Bewertungs-
// Passwort inkl. Bewerten-Recht) + weiter zur gewünschten Seite;
// daneben → 401, nach pwMaxFails Fehlversuchen pro IP 429 ohne Vergleich.
func (a *Auth) accessSubmit(w http.ResponseWriter, r *http.Request) {
	if a.accessPass == nil && a.scorePass == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.errorPage(w, http.StatusBadRequest, "Zugang fehlgeschlagen", "Ungültige Anfrage.")
		return
	}
	next := sanitizeNext(r.Form.Get("next"))
	ip := clientIP(r)
	if wait := a.pwBlocked(ip); wait > 0 {
		a.accessPage(w, http.StatusTooManyRequests,
			fmt.Sprintf("Zu viele Fehlversuche – bitte %d Sek. warten.",
				int(wait.Seconds())+1), next)
		return
	}
	pw := []byte(r.Form.Get("password"))
	bewerten := false
	ok := false
	// Das Bewertungs-Passwort wird zuerst verglichen: es öffnet mehr
	// (Lesen + Bewerten) und darf bewusst mit dem Zugangspasswort
	// identisch sein – dann öffnet die Eingabe beides.
	if a.scorePass != nil && subtle.ConstantTimeCompare(pw, a.scorePass) == 1 {
		ok, bewerten = true, true
	} else if a.accessPass != nil && subtle.ConstantTimeCompare(pw, a.accessPass) == 1 {
		ok = true
	}
	if ok {
		a.pwReset(ip)
		v, err := a.signAccess(time.Now().Add(a.sessionTTL), bewerten)
		if err != nil {
			a.serverError(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: accessCookie, Value: v,
			Path: "/", MaxAge: int(a.sessionTTL.Seconds()),
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure,
		})
		what := "Lese-Zugang"
		if bewerten {
			what = "Bewerten"
		}
		log.Printf("Zugangs-Passwort akzeptiert (%s, IP %s)", what, ip)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	blocked := a.pwFail(ip)
	log.Printf("Zugangs-Passwort falsch (IP %s)", ip)
	msg := "Passwort falsch."
	if blocked {
		msg = "Passwort falsch – und damit zu viele Fehlversuche: 60 Sek. Pause."
	}
	a.accessPage(w, http.StatusUnauthorized, msg, next)
}

// accessPage rendert das minimalistische Zugangs-Formular – wie die
// errorPage ohne Base-Template, damit es auch vor jedem Login trägt. Bei
// konfiguriertem Provider sitzt unter dem Passwortfeld der Login-Button:
// OAuth geht also immer direkt auf der ersten Seite.
func (a *Auth) accessPage(w http.ResponseWriter, status int, msg, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fehler := ""
	if msg != "" {
		fehler = `<p class="auth-fail">` + template.HTMLEscapeString(msg) + `</p>`
	}
	hilfe := "Diese Turnier-Ansicht ist passwortgeschützt."
	placeholder := "Zugangspasswort"
	if a.scorePass != nil {
		hilfe += " Das Zugangspasswort öffnet die reine Lese-Ansicht, das " +
			"Bewertungs-Passwort zusätzlich die Punkteeingabe."
		placeholder = "Zugangs- oder Bewertungs-Passwort"
	} else {
		hilfe += " Das Passwort öffnet die reine Lese-Ansicht."
	}
	oauth := ""
	if a.hasProvider {
		hilfe += " Alternativ kannst du dich beim Login-Anbieter anmelden " +
			"(freigeschaltete Adressen dürfen damit bewerten)."
		oauth = fmt.Sprintf(
			`<a class="btn primary" href="/auth/login?next=%s">Beim Login-Anbieter anmelden</a>`,
			template.HTMLEscapeString(url.QueryEscape(next)))
	}
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gardetanzturnier · Zugang</title>
<link rel="icon" type="image/svg+xml" href="/static/garde-icon.svg">
<link rel="icon" type="image/png" href="/static/garde-icon.png">
<link rel="stylesheet" href="/static/style.css"></head>
<body>
<div class="wrap auth-error">
  <div class="card">
    <h1>Zugang</h1>
    %s
    <p>%s</p>
    <form method="post" action="/access">
      <input type="hidden" name="next" value="%s">
      <input type="password" name="password" placeholder="%s"
             autocomplete="current-password" autofocus>
      <button class="btn primary" type="submit">Anzeigen</button>%s
    </form>
  </div>
</div>
</body></html>`, fehler, template.HTMLEscapeString(hilfe),
		template.HTMLEscapeString(next), placeholder, oauth)
}

// ---------- Fehlerseiten ----------

func (a *Auth) errorPage(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gardetanzturnier · %s</title>
<link rel="icon" type="image/svg+xml" href="/static/garde-icon.svg">
<link rel="icon" type="image/png" href="/static/garde-icon.png">
<link rel="stylesheet" href="/static/style.css"></head>
<body>
<div class="wrap auth-error">
  <div class="card">
    <h1>%s</h1>
    <p>%s</p>
    <p><a class="btn primary" href="/">Zurück zur Live-Ansicht</a></p>
  </div>
</div>
</body></html>`,
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(title), msg)
}

func (a *Auth) serverError(w http.ResponseWriter, err error) {
	log.Printf("Auth-Fehler: %v", err)
	a.errorPage(w, http.StatusInternalServerError, "Interner Fehler",
		"Beim Login ist ein interner Fehler aufgetreten. Details im Server-Log.")
}
