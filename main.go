// tuniert – Live-Wertung für Gardetanz-Turniere.
//
// Go + HTMX als Single-Binary: SQLite (ohne CGO), HTMX & Templates eingebettet.
// Bei leerer Datenbank wird die eingebettete Auslosungsliste automatisch
// importiert (internal/store/seed).
package main

import (
	"embed"
	"flag"
	"log"
	"net/http"

	"tuniert/internal/store"
	"tuniert/internal/web"
)

//go:embed web
var assets embed.FS

func main() {
	addr := flag.String("addr", ":8080", "HTTP-Adresse")
	db := flag.String("db", "tuniert.db", "Pfad zur SQLite-Datei")
	flag.Parse()

	st, err := store.Open(*db)
	if err != nil {
		log.Fatalf("Datenbank öffnen: %v", err)
	}
	defer st.Close()

	if n, err := st.SeedIfEmpty(); err != nil {
		log.Printf("Seed-Import fehlgeschlagen: %v", err)
	} else if n > 0 {
		log.Printf("Leere Datenbank – eingebettete Auslosungsliste importiert (%d Zeilen)", n)
	}

	app, err := web.New(st, assets)
	if err != nil {
		log.Fatalf("Web-App initialisieren: %v", err)
	}

	log.Printf("tuniert läuft auf http://localhost%s (DB: %s)", *addr, *db)
	log.Fatal(http.ListenAndServe(*addr, app.Handler()))
}
