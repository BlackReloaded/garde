// turnier – Live-Wertung für Gardetanz-Turniere.
//
// Go + HTMX als Single-Binary: SQLite (ohne CGO), HTMX & Templates eingebettet.
// Daten werden manuell per CSV-Import geladen (Admin → CSV Import).
// HTTPS optional eingebaut (-tls-domain): TLS-Terminierung + automatische
// Let's-Encrypt-Zertifikate via autocert – ohne Reverse Proxy davor.
//
// Login: OIDC (OAuth2-Code-Flow) oder generisches OAuth2 (z. B. der
// eingebaute Provider von mailcow) – ohne Konfiguration startet die App NICHT.
// Rollen (admin/bewerten) werden über die Datenbank vergeben (Admin-UI);
// nur dort gelistete Benutzer dürfen etwas ändern, alle anderen haben
// reines Lese-Recht.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"turnier/internal/model"
	"turnier/internal/store"
	"turnier/internal/web"
)

//go:embed web
var assets embed.FS

func main() {
	addr := flag.String("addr", ":8080",
		"HTTP-Adresse (ohne -tls-domain = App); mit -tls-domain Listener für "+
			"ACME-Challenges + Redirect auf HTTPS (Docker: Host-Port 80 → 8080)")
	db := flag.String("db", "turnier.db", "Pfad zur SQLite-Datei")

	tlsDomain := flag.String("tls-domain", "",
		"HTTPS aktivieren: öffentlicher Hostname der App (z. B. turnier.example.de). "+
			"Zertifikate holt und erneuert die App automatisch über Let's Encrypt "+
			"(HTTP-01 über -addr, TLS-ALPN über -https-addr). Leer = plain HTTP "+
			"(für Offline-/LAN-Einsätze leer lassen – LE signiert nur DNS-Namen, "+
			"keine nackten IPs; für IPv6-only genügt ein AAAA-Record).")
	httpsAddr := flag.String("https-addr", ":8443",
		"HTTPS-Bind-Adresse (nur mit -tls-domain); die App muss öffentlich unter "+
			"https://<tls-domain> erreichbar sein (Docker: Host-Port 443 → 8443)")
	tlsCache := flag.String("tls-cache", "acme-cache",
		"Verzeichnis für Let's-Encrypt-Zertifikate (persistieren, z. B. im "+
			"Docker-Volume /data – dann überlebt der Cache den Container-Neubau)")

	issuer := flag.String("oidc-issuer", "",
		"OIDC-Issuer (Discovery-URL des Anbieters), z. B. https://accounts.google.com – "+
			"genau einer von -oidc-issuer oder -oauth2-base-url – Pflicht")
	oauth2BaseURL := flag.String("oauth2-base-url", "",
		"Base-URL eines generischen OAuth2-Providers, z. B. https://mail.kohlbau.de "+
			"(mailcow; Endpoints /oauth/authorize|token|profile) – "+
			"genau einer von -oidc-issuer oder -oauth2-base-url – Pflicht")
	oauth2AuthorizeURL := flag.String("oauth2-authorize-url", "",
		"Override des Authorize-Endpunkts (Default: <oauth2-base-url>/oauth/authorize)")
	oauth2TokenURL := flag.String("oauth2-token-url", "",
		"Override des Token-Endpunkts (Default: <oauth2-base-url>/oauth/token)")
	oauth2ProfileURL := flag.String("oauth2-profile-url", "",
		"Override des Profile-Endpunkts (Default: <oauth2-base-url>/oauth/profile)")
	oauth2Scope := flag.String("oauth2-scope", "profile",
		"Scope für den generischen OAuth2-Modus (mailcow: profile)")
	clientID := flag.String("oidc-client-id", "",
		"OAuth2-Client-ID der im Anbieter registrierten turnier-App – Pflicht")
	clientSecret := flag.String("oidc-client-secret", os.Getenv("TURNIER_OIDC_CLIENT_SECRET"),
		"OAuth2-Client-Secret (bzw. Env TURNIER_OIDC_CLIENT_SECRET) – Pflicht")
	redirectURL := flag.String("oidc-redirect-url", "http://localhost:8080/auth/callback",
		"Callback-URL (muss exakt so im Anbieter registriert sein)")
	admins := flag.String("admins", "",
		"Optional (v. a. beim ersten Start): Komma-Liste von E-Mail-Adressen, die "+
			"beim Start als admin in die Datenbank übernommen werden – idempotent, "+
			"entfernt nichts. Danach gilt nur die DB (Admin → Berechtigungen)")
	sessionHours := flag.Float64("session-hours", 12,
		"Session-Laufzeit in Stunden")
	flag.Parse()

	switch {
	case *issuer == "" && *oauth2BaseURL == "":
		log.Fatal("Login-Provider ist Pflicht: entweder -oidc-issuer (OIDC) oder " +
			"-oauth2-base-url (generisches OAuth2, z. B. mailcow) setzen – " +
			"ohne Auth startet turnier nicht.")
	case *issuer != "" && *oauth2BaseURL != "":
		log.Fatal("Nur EIN Login-Provider ist erlaubt: -oidc-issuer und " +
			"-oauth2-base-url dürfen nicht gleichzeitig gesetzt sein.")
	case *clientID == "" || *clientSecret == "":
		log.Fatal("Login-Client ist Pflicht: -oidc-client-id und -oidc-client-secret " +
			"setzen (Secret alternativ über Env TURNIER_OIDC_CLIENT_SECRET).")
	}

	st, err := store.Open(*db)
	if err != nil {
		log.Fatalf("Datenbank öffnen: %v", err)
	}
	defer st.Close()

	// -admins: Adressen als admin in die DB übernehmen (idempotent – nur
	// fehlende Einträge, nichts wird entfernt). Danach ist die DB die
	// einzige Rollen-Quelle (Admin → Berechtigungen).
	for _, e := range splitEmails(*admins) {
		if !validEmailAddr(e) {
			log.Printf("WARNUNG: -admins enthält ungültige Adresse, übersprungen: %q", e)
			continue
		}
		if err := st.AddLoginRole(model.RoleAdmin, e); err != nil {
			log.Fatalf("Rollen-Liste übernehmen: %v", err)
		}
	}

	auth, err := web.NewAuth(st, web.AuthConfig{
		Issuer:             *issuer,
		OAuth2BaseURL:      *oauth2BaseURL,
		OAuth2AuthorizeURL: *oauth2AuthorizeURL,
		OAuth2TokenURL:     *oauth2TokenURL,
		OAuth2ProfileURL:   *oauth2ProfileURL,
		OAuth2Scope:        *oauth2Scope,
		ClientID:           *clientID,
		ClientSecret:       *clientSecret,
		RedirectURL:        *redirectURL,
		SessionTTL:         time.Duration(*sessionHours * float64(time.Hour)),
	})
	if err != nil {
		log.Fatalf("Auth initialisieren: %v", err)
	}

	app, err := web.New(st, assets, auth)
	if err != nil {
		log.Fatalf("Web-App initialisieren: %v", err)
	}

	provider := fmt.Sprintf("OIDC %s", *issuer)
	if *oauth2BaseURL != "" {
		provider = fmt.Sprintf("OAuth2 %s", strings.TrimRight(*oauth2BaseURL, "/"))
	}
	runAddr := "http://localhost" + *addr
	if *tlsDomain != "" {
		runAddr = "https://" + *tlsDomain
	}
	log.Printf("turnier läuft auf %s (DB: %s)", runAddr, *db)
	log.Printf("Login-Provider: %s (Callback: %s)", provider, *redirectURL)

	// Rollen-Status (DB) loggen; bei leerer Admin-Liste warnen – sonst
	// könnte sich niemand anmelden und man wüsste beim Start nicht warum.
	roles, _ := st.ListLoginRoles()
	nAdmin, nScorer := 0, 0
	for _, r := range roles {
		switch r.Role {
		case model.RoleAdmin:
			nAdmin++
		case model.RoleScorer:
			nScorer++
		}
	}
	log.Printf("Rollen-Liste (DB): %d admin, %d bewerten", nAdmin, nScorer)
	if nAdmin == 0 {
		log.Printf("WARNUNG: Es gibt noch KEINEN Admin in der Datenbank – niemand " +
			"kann sich anmelden. Beim Start mit -admins setzen (übernimmt die " +
			"Adressen in die DB) oder danach in der Admin-UI (Berechtigungen).")
	}
	handler := app.Handler()
	if *tlsDomain != "" {
		// TLS-Terminierung in der App (autocert): Beim ersten HTTPS-Kontakt
		// wird das Zertifikat bei Let's Encrypt geholt und danach automatisch
		// erneuert. Der HTTP-Listener beantwortet ACME HTTP-01 und leitet den
		// Rest auf HTTPS um. Ohne erreichbaren ACME-Zugang (und ohne Cache)
		// entsteht kein Zertifikat – für Offline-Einsätze daher ohne
		// -tls-domain starten.
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*tlsDomain),
			Cache:      autocert.DirCache(*tlsCache),
		}
		tlsSrv := &http.Server{
			Addr:      *httpsAddr,
			Handler:   handler,
			TLSConfig: mgr.TLSConfig(),
		}
		go func() {
			if err := tlsSrv.ListenAndServeTLS("", ""); err != nil {
				log.Fatalf("HTTPS-Server (%s): %v", *httpsAddr, err)
			}
		}()
		log.Printf("HTTPS: App auf %s – HTTP-Listener %s = ACME-Challenges + Redirect "+
			"(Cert-Cache: %s)", *httpsAddr, *addr, *tlsCache)
		log.Fatal(http.ListenAndServe(*addr, mgr.HTTPHandler(nil)))
	}
	log.Fatal(http.ListenAndServe(*addr, handler))
}

// splitEmails zerlegt eine Komma-Liste in normalisierte (klein, getrimmte)
// E-Mail-Adressen.
func splitEmails(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// validEmailAddr ist ein leichter Plausibilitätscheck (genau ein @, beide
// Seiten gefüllt) für die -admins-Adresse beim Start-Seed.
func validEmailAddr(e string) bool {
	if len(e) == 0 || len(e) > 254 || strings.Count(e, "@") != 1 {
		return false
	}
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-1
}
