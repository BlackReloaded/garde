# tuniert

Live-Wertung für Gardetanz-Turniere als **Single-Binary**: Go +
[HTMX](https://htmx.org) + SQLite ([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite),
ohne CGO) + Login per OIDC oder generischem OAuth2, z. B. mailcow
(`golang.org/x/oauth2`, `coreos/go-oidc`). Templates, CSS und
HTMX sind via `//go:embed` im Binary eingebettet – lauffähig auch auf Messe-PCs
**ohne Internetanschluss** (siehe [Login & Rechte](#login--rechte)).

## Aufbau

```
tuniert
├── main.go                     # Entry-Point, //go:embed web, Flags
├── internal/
│   ├── model/                  # Datenmodell + Wertungslogik (Drop-High/Low, Tiebreak, Ränge)
│   ├── schedule/               # ETA-Berechnung aus den Noten-Zeitstempeln
│   └── store/                  # SQLite-Zugriff, CSV-Import/-Export
│   └── web/                    # HTTP-Router, Go-Template-Rendering (Live + Admin)
└── web/
    ├── templates/              #   base.html, live.html, admin.html, parts/*
    └── static/                 #   htmx.min.js (2.0.4), style.css
```

## Starten

```sh
go build -o tuniert .
```

Beispiel mit **mailcow** als Login-Provider (empfohlen, s. unten):

```sh
./tuniert \
  -oauth2-base-url https://mail.kohlbau.de \
  -oidc-client-id tuniert \
  -oidc-client-secret <geheim> \
  -admins "orga1@verein.de,orga2@verein.de"
```

> `-admins` nur **beim ersten Start** nötig (seedet die Admin-Adressen in
> die DB, idempotent). Danach reicht das Flag wegzulassen – Rollen werden
> in der Admin-UI (Admin → *Berechtigungen*) gepflegt.

Beispiel mit einem **OIDC-Provider** (Keycloak, Google, …):

```sh
./tuniert \
  -oidc-issuer https://idp.example.com \
  -oidc-client-id tuniert \
  -oidc-client-secret <geheim> \
  -admins "orga1@verein.de,orga2@verein.de"
```

- `-addr` (Default `:8080`): HTTP-Adresse
- `-db` (Default `tuniert.db`): Pfad zur SQLite-Datei

## Login & Rechte

**Ohne Login-Konfiguration startet die App nicht** – Datenänderungen sind immer
login-pflichtig. Genau **ein** Provider wird konfiguriert (beide Flags nicht
gleichzeitig):

1. **Generisches OAuth2** (empfohlen, z. B. mailcow):
   `-oauth2-base-url https://mail.kohlbau.de` – Endpoints
   `/oauth/authorize`, `/oauth/token`, `/oauth/profile` werden daraus
   abgeleitet (Override: `-oauth2-authorize-url`, `-oauth2-token-url`,
   `-oauth2-profile-url`, Scope: `-oauth2-scope`, Default `profile`).
   Flow: Authorization-Code-Flow mit State (kein PKCE/Nonce – der Provider
   kennt beides nicht), dann Benutzerdaten per Profile-Endpunkt.
2. **OIDC**: `-oidc-issuer <Discovery-URL>` (Keycloak, Google, …) –
   Authorization-Code-Flow mit PKCE, State + Nonce, ID-Token-Verifizierung.

Gemeinsam (Pflicht für beide Modi):

- `-oidc-client-id`: Client-ID der registrierten tuniert-App
- `-oidc-client-secret` (Pflicht): Client-Secret – besser per Env
  `TUNIERT_OIDC_CLIENT_SECRET` (taucht sonst in der Prozessliste auf)
- `-oidc-redirect-url` (Default `http://localhost:8080/auth/callback`):
  Callback-URL, **muss exakt so im Provider registriert sein**
- `-admins` (optional, v. a. erster Start): Komma-Liste E-Mail-Adressen, die
  beim Start als *admin* in die Datenbank übernommen werden (idempotent –
  fügt nur fehlende hinzu, entfernt nichts). Danach ist die DB die einzige
  Rollen-Quelle.
- `-session-hours` (Default `12`): Session-Laufzeit

**Rollen liegen in der Datenbank** (Tabelle `login_role`, gefüllt über die
Admin-UI) und werden dort gepflegt:

| Status | Rechte |
|---|---|
| nicht angemeldet | reines Lese-Recht: Live-Ansicht, Ranglisten, Zeitplan |
| angemeldet (in der DB als *bewerten*) | + Noten eintragen (`POST /score`) |
| angemeldet (in der DB als *admin*) | + kompletter Admin-Bereich |

Nicht in der DB → Login wird im Callback abgewiesen (403-Seite). Die Rolle
wird bei **jedem Request** gegen die aktuelle DB-Liste geprüft: Wird eine
Adresse entfernt, verliert die Session sofort ihre Rechte.

**Rollen in der Admin-UI verwalten** (Admin → *Berechtigungen*):
E-Mail-Adressen als *admin* oder *bewerten* **hinzufügen** und wieder
**entfernen** (auch die per `-admins` gesetzten – die sind danach normale
DB-Einträge). Wirkt **sofort** (nächster Request prüft die Rolle neu) –
ohne Neustart. Nur der Admin kann die Listen ändern
(Routen `POST /admin/role/add` bzw. `/admin/role/remove`).

> **Hinweis:** Startet die App mit leerer DB und ohne `-admins`, gibt es
> noch keinen Admin – der Start loggt eine Warnung. Dann `-admins` beim
> nächsten Start setzen (seedet die DB) oder per DB-Datei pflegen.

### Login mit mailcow (empfohlen)

mailcow hat einen **eingebauten OAuth2-Provider** (kein OIDC – daher
`-oauth2-base-url` statt `-oidc-issuer`). Kein Zusatzsoftware, und wer in
mailcow 2FA/WebAuthn aktiviert hat, schützt damit auch den tuniert-Login.

Einmalig einrichten (mailcow-UI, Admin):

1. *Configuration → Access → OAuth2 → **Add OAuth2 client***
2. **Redirect URI** exakt wie der tuniert-Flag `-oidc-redirect-url`
   setzen, z. B. `http://192.168.1.50:8080/auth/callback` (lokale Nutzung)
   oder `https://tuniert.verein.de/auth/callback` (Web-Hosting).
3. Client-ID + Secret aus der UI übernehmen → Start:

```sh
./tuniert \
  -oauth2-base-url https://mail.kohlbau.de \
  -oidc-client-id tuniert \
  -oidc-client-secret <geheim> \
  -admins "orga1@verein.de"   # nur beim ersten Start (seedet die DB)
```

Login-UX: Der User wird zur **mailcow-Login-Seite** umgeleitet (inkl. 2FA,
falls aktiviert), dort eine **Consent-Seite** bestätigen („tuniert darf sich
anmelden?") und landet wieder in der App. Identität = Mailbox-Adresse; nur
Adressen, die in der DB-Rollen-Liste stehen, bekommen Rechte (siehe oben).
Die Richter (*bewerten*) legt der Admin dann in der Admin-UI an.

Weitere Anbieter (Google, Apple, Keycloak, …) gehen später als **OIDC-Modus**
(`-oidc-issuer`); zur Zeit wird genau ein Provider unterstützt.

**Offline-Verhalten:** Die App startet und die Live-Ansicht (Lese-Recht)
funktioniert auch, wenn der Provider nicht erreichbar ist (z. B. kein
Internet auf dem Messe-PC). Bereits eingeloggte Sessions laufen weiter
(12 h); nur neue Logins scheitern, bis der Provider erreichbar ist.

**Daten laden:** Die App startet mit leerer Datenbank – es gibt **keinen
automatischen Seed**. Auslosungslisten werden manuell per CSV-Import geladen
(Admin → CSV Import, Format siehe unten).
Durchgestrichene PDF-Einträge (Zugänge) werden als `withdrawn` importiert und
im Admin-Bereich per Checkbox umschaltbar. In der Live-Ansicht erscheinen sie
im Zeitplan **gestrichen** (Badge „gestrichen“, ohne Zeit) und werden bei der
Zeitprognose **übersprungen** – kein Slot, keine ETA, nicht als Nächstes.

## Wertungsregeln (implementiert)

- 7 Richter, ganzzahlige Noten **0–100**.
- **Höchste und niedrigste Note werden gestrichen**, die mittleren 5 summiert.
- Die gestrichenen Noten bleiben in der Tabelle sichtbar (durchgestrichen, grau).
- **Tiebreak:** Zuerst die gestrichenen Noten (üblicherweise die höchste)
  vergleichen, danach alle 7 Richter-Noten. Bei Gleichstand: **gleicher Platz**,
  der nächste Platz wird übersprungen (1, 1, 3).
- **Stern-Finalisten** (im PDF mit `*` markiert) erhalten eine **eigene
  Platzierung** neben der regulären.
- Unvollständige Wertungen (n/7) erscheinen grau im Bereich „In Bewertung"
  mit Fortschritts-Badge, nicht in der Platzierung.

## Zeitplanung / ETAs

Es gibt **keine Start-/Stopp-Buttons** – alle Zeitangaben werden ausschließlich
aus den Zeitstempeln der eingegebenen Noten abgeleitet:

- **Slot-Beobachtung:** Abstand zwischen den 7. Noten zweier direkt
  aufeinander folgender (komplett bewerteter) Tänzer.
- **Punktezeit-Beobachtung:** letztes minus erstes Noten-Zeitstempel eines
  komplett bewerteten Tänzers (Teilnoten fließen nicht ein, da unvollständig).
- **Falls keine Beobachtungen:** `Slot = Aufritt (60 s) + max. Tanz
  (120 s) + Punktezeit (120 s)`, d. h. Default 5:00.
- **Aktueller Tänzer:** Restzeit = `Punktezeit · (7 − n) / 7` (bei n Noten);
  bei 0 Noten: Ende ≈ jetzt + Slot.
- **Folgende Tänzer k:** `Start_k = Ende_{k−1}`, `Ende_k = Start_k + Slot`
  (nachfolgende Slots starten, sobald der vorherige fertig ist).
- **Block-Ende:** Ende des letzten Tänzers.

Alle Werte aktualisieren sich live mit jedem Poll (5 s) und nach jeder
Score-Eingabe (HTMX OOB-Swap). In der Kopfzeile ist der verwendete
`Ø Slot`-Schätzwert eingeblendet.

## CSV-Import / Export (Admin-Bereich)

```
session;turnier;disziplin;startnr;name;verein;star;withdrawn
2026/27;Stadtmeisterschaft 15.03.2026;Jugend Garde;3;Beispielgruppe;Tanzverein Beispiel e.V.;0;0
```

- Trenner `;` oder `,`; Kopfzeile optional; Flags `1`/`x`/`*`/`ja` = wahr.
- **Zeilenreihenfolge = Startreihenfolge.**
- `startnr` ist **Text** (`001` ≠ `1`).
- Import ist **idempotent**: Session/Turnier/Disziplin werden nach Name
  (case-insensitiv) angelegt oder wiederverwendet; Teilnehmer werden pro
  (Disziplin, Startnummer) neu angelegt bzw. aktualisiert.
- Export: `GET /admin/csv/export?s=<session-id>` (Download).

## Datenmodell

```
Session ──> Competition („Turnier") ──> Discipline (Disziplin)
                                        ├── max_dance_sec  = 120  (max. Tanzlänge)
                                        ├── scoring_def_sec= 120  (Default Punktezeit)
                                        ├── walk_on_sec    = 60   (Aufritt/Wechsel)
                                        └── num_judges     = 7
Participant (start_nr TEXT, name, verein, starred, withdrawn, sort_order)
  └── Score (judge 1..n, value 0..100, updated_at; UNIQUE(participant, judge), Upsert)

LoginUser    (provider, provider_sub, UNIQUE(provider+sub); email, name, role,
              created_at, last_login_at)
LoginSession (token TEXT PK, user_id FK→login_user, created_at, expires_at)
LoginRole    (role TEXT, email TEXT, created_at; PK(role+email))
              └─ UI-verwaltete Rollen (Admin → Berechtigungen); ergänzt die
                 Start-Flags, wirkt sofort
```

`Session` bezeichnet ein Jahr (eine Saison), z. B. `2026/27`.

Zugelassene Disziplinen werden einheitlich mit 7 Richtern bewertet – auch
Schautanz. `withdrawn`-Teilnehmer werden aus Live-Scoring, Zeitprognose
(ETAs/Slots) und Platzierungen ausgeschlossen, aber im Zeitplan gestrichen
angezeigt – und im Admin-Bereich mit Badge sichtbar.

## Weboberfläche

| Route | Zweck |
|---|---|
| `GET /` | Live-Ansicht (Session/Turnier/Disziplin via Query `s/t/d` + Cookie) – öffentlich (Lese-Recht) |
| `GET /partials/refresh` | OOB-Partial-Updates (Status, beide Platzierungen, Zeitplan) – Polling alle 5 s; Score-Panel nur mit explizitem `sel` (wird beim Polling nicht neu gerendert, damit Fokus/Eingabe erhalten bleiben) |
| `POST /score` | `participant_id`, `judge`, `value` (leer = Note löschen); antwortet mit OOB-Updates – **nur mit Session (Rolle bewerten/admin)** |
| `GET /admin`, `POST /admin/*` | Verwaltung: Session/Turnier/Disziplin anlegen · umbenennen · löschen, Disziplin-Zeiten, Teilnehmer (inline bearbeiten, ↑↓ verschieben, löschen), Stern/Zurückgezogen, Noten-Reset, CSV-Import/-Export – **nur mit Session (Rolle admin)** |
| `GET /auth/login` | Login-Start (Redirect zum Provider, `next=` für Ziel-URL) |
| `GET /auth/callback` | Callback: State (OIDC: +Nonce/PKCE) prüfen, Identität per ID-Token bzw. Profile-Endpunkt, Rolle aus Allowlist, Session-Cookie setzen |
| `POST /auth/logout` | Session löschen (POST gegen <img>-CSRF) |
| `GET /healthz` | Health-Check |

Nicht angemeldet: Score-Panel zeigt nur „Nur Lese-Recht … anmelden"; der
Admin-Link ist nur für Admins sichtbar; alle geschützten Routen leiten auf
`/auth/login` (HTMX folgt dem Redirect).

**Score-Panel:** Zeigt standardmäßig den aktuellen Tänzer; über das Select
können auch bereits bewertete Tänzer zum Korrigieren ausgewählt werden
(Auswahl bleibt bis zum Abschluss – dann automatisch zurück zum aktuellen).
Enter im Feld speichert und fokussiert das nächste freie Feld; leeres Feld +
Enter löscht die Note.

## Entwicklung

```sh
go build ./...
go vet ./...
go test ./...
```

Tests decken die Wertungslogik (Drop-High/Low, Tiebreaks, Ränge mit
Gleichstand), die ETA-Berechnung (mit und ohne Beobachtungen) sowie das
Auth-Modell ab: Rollen-Guards, State-Payload-Signatur, Provider-Modus-
Validierung und beide kompletten Login-Flows (Login → Provider → Callback):
OIDC gegen einen in-Test-Fake-Provider mit RS256-signierten ID-Tokens und
generisches OAuth2 (mailcow-Style: authorize/token/profile, ohne
Discovery/ID-Token).
