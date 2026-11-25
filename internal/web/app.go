// Package web enthält die HTTP-Ansicht (Go Templates + HTMX).
package web

import (
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"

	"turnier/internal/model"
	"turnier/internal/store"
)

// Go's MIME-Tabelle kennt .webmanifest nicht (würde text/plain liefern) –
// ohne korrekten Typ verwerfen manche Browser das PWA-Manifest.
func init() {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// App bündelt Store, Templates, statische Assets und Auth.
type App struct {
	store  *store.Store
	tmpl   map[string]*template.Template // "live", "admin"
	static http.Handler
	auth   *Auth
}

// New erzeugt die App. assets ist das eingebettete FS (Repo-Root, mit web/).
func New(st *store.Store, assets fs.FS, auth *Auth) (*App, error) {
	tmpl := map[string]*template.Template{}
	for _, page := range []string{"live", "admin"} {
		t, err := template.New("base.html").Funcs(funcs).ParseFS(assets,
			"web/templates/base.html",
			"web/templates/"+page+".html",
			"web/templates/parts/*.html")
		if err != nil {
			return nil, err
		}
		tmpl[page] = t
	}
	sub, err := fs.Sub(assets, "web/static")
	if err != nil {
		return nil, err
	}
	return &App{
		store: st,
		tmpl:  tmpl,
		auth:  auth,
		// FileServerFS erwartet den Pfad relativ zum FS-Root – das /static-
		// Präfix der Route muss hier abgestreift werden, sonst 404.
		static: http.StripPrefix("/static/", http.FileServerFS(sub)),
	}, nil
}

// Handler registriert alle Routen.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	// Lese-Zugang: ohne -password offen wie bisher. Mit Zugangs-Passwort
	// brauchen anonyme Requests erst das Passwort (Cookie /access);
	// Sessions mit Rolle passieren die Prüfung direkt.
	gate := a.auth.requireAccess
	mux.HandleFunc("GET /{$}", gate(a.livePage))
	// Polling ohne sel → Panel bleibt stehen (Fokus/Eingabe erhalten);
	// sel-Wechsel im Panel → Panel wird mitgerendert.
	mux.HandleFunc("GET /partials/refresh", gate(func(w http.ResponseWriter, r *http.Request) {
		a.refresh(w, r, explicitSel(r) != 0, false)
	}))
	mux.HandleFunc("GET /access", a.auth.accessForm)
	mux.HandleFunc("POST /access", a.auth.accessSubmit)
	// Ergebnisprotokoll (Wertungsheft) – je eine Disziplin und ein ganzes
	// Turnier, jeweils auch als CSV. Lese-Recht wie die Live-Ansicht selbst,
	// funktioniert ohne Zugangs-Passwort auch ohne jede Anmeldung.
	mux.HandleFunc("GET /export/protocol", gate(a.exportProtocol))
	mux.HandleFunc("GET /export/competition", gate(a.exportProtocol))
	mux.HandleFunc("GET /export/protocol.csv", gate(a.exportResultsCSV))
	mux.HandleFunc("GET /export/competition.csv", gate(a.exportResultsCSV))
	// Änderungen brauchen eine Session: Noten → Rolle bewerten (oder admin),
	// Admin-Bereich → Rolle admin. Nicht angemeldet bleibt die reine
	// Live-Ansicht (Lese-Recht).
	score := a.auth.requireRole(model.RoleScorer, a.score)
	mux.HandleFunc("POST /score", score)

	admin := func(h http.HandlerFunc) http.HandlerFunc {
		return a.auth.requireRole(model.RoleAdmin, h)
	}
	mux.HandleFunc("GET /admin", admin(a.adminPage))
	mux.HandleFunc("POST /admin/session/create", admin(a.adminSessionCreate))
	mux.HandleFunc("POST /admin/session/rename", admin(a.adminSessionRename))
	mux.HandleFunc("POST /admin/session/delete", admin(a.adminSessionDelete))
	mux.HandleFunc("POST /admin/comp/create", admin(a.adminCompCreate))
	mux.HandleFunc("POST /admin/comp/rename", admin(a.adminCompRename))
	mux.HandleFunc("POST /admin/comp/times", admin(a.adminCompTimes))
	mux.HandleFunc("POST /admin/block/create", admin(a.adminBlockCreate))
	mux.HandleFunc("POST /admin/block/update", admin(a.adminBlockUpdate))
	mux.HandleFunc("POST /admin/block/delete", admin(a.adminBlockDelete))
	mux.HandleFunc("POST /admin/comp/delete", admin(a.adminCompDelete))
	mux.HandleFunc("POST /admin/disc/create", admin(a.adminDiscCreate))
	mux.HandleFunc("POST /admin/disc/update", admin(a.adminDiscUpdate))
	mux.HandleFunc("POST /admin/disc/delete", admin(a.adminDiscDelete))
	mux.HandleFunc("POST /admin/part/create", admin(a.adminPartCreate))
	mux.HandleFunc("POST /admin/part/update", admin(a.adminPartUpdate))
	mux.HandleFunc("POST /admin/part/move", admin(a.adminPartMove))
	mux.HandleFunc("POST /admin/part/delete", admin(a.adminPartDelete))
	mux.HandleFunc("POST /admin/scores/reset", admin(a.adminScoresReset))
	mux.HandleFunc("POST /admin/csv/import", admin(a.adminCSVImport))
	mux.HandleFunc("GET /admin/csv/export", admin(a.adminCSVExport))
	mux.HandleFunc("POST /admin/role/add", admin(a.adminRoleAdd))
	mux.HandleFunc("POST /admin/role/remove", admin(a.adminRoleRemove))

	mux.HandleFunc("GET /auth/login", a.auth.login)
	mux.HandleFunc("GET /auth/callback", a.auth.callback)
	mux.HandleFunc("POST /auth/logout", a.auth.logout)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", a.static)

	return withLogging(mux)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || len(r.URL.Path) > 7 && r.URL.Path[:7] == "/static" {
			next.ServeHTTP(w, r)
			return
		}
		log.Printf("%s %s", r.Method, r.URL.String())
		next.ServeHTTP(w, r)
	})
}
