// Package web enthält die HTTP-Ansicht (Go Templates + HTMX).
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"

	"tuniert/internal/store"
)

// App bündelt Store, Templates und statische Assets.
type App struct {
	store  *store.Store
	tmpl   map[string]*template.Template // "live", "admin"
	static http.Handler
}

// New erzeugt die App. assets ist das eingebettete FS (Repo-Root, mit web/).
func New(st *store.Store, assets embed.FS) (*App, error) {
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
		store:  st,
		tmpl:   tmpl,
		// FileServerFS erwartet den Pfad relativ zum FS-Root – das /static-
		// Präfix der Route muss hier abgestreift werden, sonst 404.
		static: http.StripPrefix("/static/", http.FileServerFS(sub)),
	}, nil
}

// Handler registriert alle Routen.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.livePage)
	// Polling ohne sel → Panel bleibt stehen (Fokus/Eingabe erhalten);
	// sel-Wechsel im Panel → Panel wird mitgerendert.
	mux.HandleFunc("GET /partials/refresh", func(w http.ResponseWriter, r *http.Request) {
		a.refresh(w, r, explicitSel(r) != 0, false)
	})
	mux.HandleFunc("POST /score", a.score)

	mux.HandleFunc("GET /admin", a.adminPage)
	mux.HandleFunc("POST /admin/session/create", a.adminSessionCreate)
	mux.HandleFunc("POST /admin/session/rename", a.adminSessionRename)
	mux.HandleFunc("POST /admin/session/delete", a.adminSessionDelete)
	mux.HandleFunc("POST /admin/comp/create", a.adminCompCreate)
	mux.HandleFunc("POST /admin/comp/rename", a.adminCompRename)
	mux.HandleFunc("POST /admin/comp/delete", a.adminCompDelete)
	mux.HandleFunc("POST /admin/disc/create", a.adminDiscCreate)
	mux.HandleFunc("POST /admin/disc/update", a.adminDiscUpdate)
	mux.HandleFunc("POST /admin/disc/delete", a.adminDiscDelete)
	mux.HandleFunc("POST /admin/part/create", a.adminPartCreate)
	mux.HandleFunc("POST /admin/part/update", a.adminPartUpdate)
	mux.HandleFunc("POST /admin/part/move", a.adminPartMove)
	mux.HandleFunc("POST /admin/part/delete", a.adminPartDelete)
	mux.HandleFunc("POST /admin/scores/reset", a.adminScoresReset)
	mux.HandleFunc("POST /admin/csv/import", a.adminCSVImport)
	mux.HandleFunc("GET /admin/csv/export", a.adminCSVExport)

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
