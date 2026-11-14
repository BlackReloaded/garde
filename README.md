# tuniert

Live-Wertung für Gardetanz-Turniere als **Single-Binary**: Go (nur Standardlib) +
[HTMX](https://htmx.org) + SQLite ([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite),
ohne CGO). Templates, CSS und HTMX sind via `//go:embed` im Binary eingebettet –
lauffähig auch auf Messe-PCs **ohne Internetanschluss**.

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
./tuniert                          # :8080, DB: ./tuniert.db
./tuniert -addr 0.0.0.0:8090 -db /daten/tuniert.db
```

- `-addr` (Default `:8080`): HTTP-Adresse
- `-db` (Default `tuniert.db`): Pfad zur SQLite-Datei

**Daten laden:** Die App startet mit leerer Datenbank – es gibt **keinen
automatischen Seed**. Auslosungslisten werden manuell per CSV-Import geladen
(Admin → CSV Import, Format siehe unten).
Durchgestrichene PDF-Einträge (Zugänge) werden als `withdrawn` importiert und
im Admin-Bereich per Checkbox umschaltbar.

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
```

`Session` bezeichnet ein Jahr (eine Saison), z. B. `2026/27`.

Zugelassene Disziplinen werden einheitlich mit 7 Richtern bewertet – auch
Schautanz. `withdrawn`-Teilnehmer werden aus Live-Scoring, ETAs und
Platzierungen ausgeschlossen (im Admin-Bereich mit Badge sichtbar).

## Weboberfläche

| Route | Zweck |
|---|---|
| `GET /` | Live-Ansicht (Session/Turnier/Disziplin via Query `s/t/d` + Cookie) |
| `GET /partials/refresh` | OOB-Partial-Updates (Status, beide Platzierungen, Zeitplan) – Polling alle 5 s; Score-Panel nur mit explizitem `sel` (wird beim Polling nicht neu gerendert, damit Fokus/Eingabe erhalten bleiben) |
| `POST /score` | `participant_id`, `judge`, `value` (leer = Note löschen); antwortet mit OOB-Updates |
| `GET /admin` | Verwaltung: Session/Turnier/Disziplin anlegen · umbenennen · löschen, Disziplin-Zeiten, Teilnehmer (inline bearbeiten, ↑↓ verschieben, löschen), Stern/Zurückgezogen, Noten-Reset, CSV-Import/-Export |
| `GET /healthz` | Health-Check |

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
Gleichstand) und die ETA-Berechnung (mit und ohne Beobachtungen) ab.
