package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"turnier/internal/model"
)

// ViewParamKey ist der Cookie-Name für die zuletzt gewählte Ansicht.
const ViewParamKey = "turnier_view"

func parseID(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// resolveView ermittelt Session/Turnier/Disziplin aus Query, Cookie und Fallback.
func (a *App) resolveView(r *http.Request) (sessionID, compID, discID int64) {
	q := r.URL.Query()
	sessionID = parseID(q.Get("s"))
	compID = parseID(q.Get("t"))
	discID = parseID(q.Get("d"))

	// Cookie als Zwischenspeicher (nur wenn Query unvollständig).
	if sessionID == 0 {
		if c, err := r.Cookie(ViewParamKey); err == nil {
			cs, ct, cd := splitView(c.Value)
			if sessionID == 0 {
				sessionID = cs
			}
			if compID == 0 {
				compID = ct
			}
			if discID == 0 {
				discID = cd
			}
		}
	}

	if sessionID == 0 {
		if sessions, err := a.store.ListSessions(); err == nil && len(sessions) > 0 {
			sessionID = sessions[0].ID
		}
	}
	if compID == 0 {
		if comps, err := a.store.ListCompetitions(sessionID); err == nil && len(comps) > 0 {
			compID = comps[0].ID
		}
	}
	if discID == 0 {
		if d, err := a.store.FirstDisciplineID(); err == nil {
			discID = d
		}
	}

	// Konsistenz: t muss zu s gehören, d muss zu t gehören.
	if c, err := a.store.GetCompetition(compID); err != nil || c.SessionID != sessionID {
		compID = 0
		discID = 0
		if comps, err := a.store.ListCompetitions(sessionID); err == nil && len(comps) > 0 {
			compID = comps[0].ID
		}
	}
	if discID != 0 {
		if d, err := a.store.GetDiscipline(discID); err != nil || d.CompetitionID != compID {
			discID = 0
		}
	}
	if discID == 0 && compID != 0 {
		if discs, err := a.store.ListDisciplines(compID); err == nil && len(discs) > 0 {
			discID = discs[0].ID
		}
	}
	return sessionID, compID, discID
}

func splitView(v string) (int64, int64, int64) {
	var s, t, d int64
	fmt.Sscanf(v, "%d-%d-%d", &s, &t, &d)
	return s, t, d
}

func (a *App) setViewCookie(w http.ResponseWriter, s, t, d int64) {
	http.SetCookie(w, &http.Cookie{
		Name: ViewParamKey, Value: fmt.Sprintf("%d-%d-%d", s, t, d),
		Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode,
	})
}

// selCookieKey merkt sich die gewählte Korrektur-Auswahl des Punkteeingabe-Panels.
const selCookieKey = "turnier_sel"

// explicitSel liest einen expliziten sel-Wert (URL oder Formular).
func explicitSel(r *http.Request) int64 {
	if s := parseID(r.URL.Query().Get("sel")); s != 0 {
		return s
	}
	return parseID(r.FormValue("sel"))
}

// panelSelection bestimmt den im Panel aktiven Teilnehmer:
// explizites sel > Cookie > gerade an der Reihe. allowComplete steuert,
// ob ein vollständig bewerteter Teilnehmer als Auswahl gilt:
//   - false (Score-POST): Die Score-Formulare senden sel mit. Wird mit
//     einer Note der 7. der Teilnehmer komplett, muss das Panel auf den
//     nächsten springen – die (mitgesendete) Auswahl darf dann nicht
//     mehr greifen.
//   - true (Seite/Refresh): Manuell gewählte Teilnehmer (Selektor, URL)
//     werden angezeigt – auch fertige (Korrekturen, offizielle Summe).
//     Das Cookie ist nur so gesetzt (siehe maybeSetSelCookie), dass eine
//     manuelle Wahl auch von Pollings erhalten bleibt.
func (a *App) panelSelection(r *http.Request, ld *LiveData, allowComplete bool) int64 {
	sel := explicitSel(r)
	if sel == 0 {
		if c, err := r.Cookie(selCookieKey); err == nil {
			sel = parseID(c.Value)
		}
	}
	if sel != 0 {
		for i := range ld.Active {
			p := ld.Active[i]
			if p.ID == sel && (allowComplete || len(p.Scores) < ld.NumJ) {
				return p.ID
			}
		}
	}
	if ld.Sched != nil && ld.Sched.Current != nil {
		return ld.Sched.Current.ID
	}
	return 0
}

// maybeSetSelCookie merkt sich eine explizite Teilnehmerwahl (nur GET –
// die Score-Formulare senden sel mit, würden aber sonst das Panel auf
// einem gerade fertiggewordenen Teilnehmer festnageln).
func (a *App) maybeSetSelCookie(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		return
	}
	if sel := explicitSel(r); sel != 0 {
		http.SetCookie(w, &http.Cookie{
			Name: selCookieKey, Value: strconv.FormatInt(sel, 10),
			Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode,
		})
	}
}

// livePage rendert die Live-Ansicht (komplette Seite).
func (a *App) livePage(w http.ResponseWriter, r *http.Request) {
	s, t, d := a.resolveView(r)
	if d == 0 {
		http.Error(w, "keine Disziplin vorhanden", http.StatusNotFound)
		return
	}
	a.setViewCookie(w, s, t, d)
	a.maybeSetSelCookie(w, r)
	ld, err := a.loadLive(d, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ld.Auth = a.auth.authState(r)
	a.finalizePanel(ld, a.panelSelection(r, ld, true))
	a.render(w, r, http.StatusOK, "live", ld)
}

// refresh liefert OOB-Partial-Updates für die Live-Ansicht (Polling + nach
// Score). withPanel steuert, ob das Score-Panel neu gerendert wird: Beim
// Hintergrund-Polling bleibt es unangetastet, damit Fokus und nicht
// abgeschlossene Eingaben im Panel erhalten bleiben. scorePost=true
// kennzeichnet die Antwort auf eine Score-POST: dort greift eine Auswahl
// eines gerade fertig gewordenen Teilnehmers nicht (Auto-Advance).
func (a *App) refresh(w http.ResponseWriter, r *http.Request, withPanel, scorePost bool) {
	_, _, d := a.resolveView(r)
	if d == 0 {
		http.Error(w, "keine Disziplin vorhanden", http.StatusNotFound)
		return
	}
	a.maybeSetSelCookie(w, r)
	ld, err := a.loadLive(d, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ld.Auth = a.auth.authState(r)
	a.finalizePanel(ld, a.panelSelection(r, ld, !scorePost))
	a.renderOOB(w, ld, withPanel)
}

// score übernimmt eine Einzelnote (value leer = löschen) ODER die
// offizielle Gesamtsumme (Feld total_override; leer = Überschreibung
// zurücksetzen).
func (a *App) score(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "ungültiges Formular", http.StatusBadRequest)
		return
	}
	pid := parseID(r.Form.Get("participant_id"))
	p, err := a.store.GetParticipant(pid)
	if err != nil {
		http.Error(w, "Teilnehmer nicht gefunden", http.StatusNotFound)
		return
	}
	d, err := a.store.GetDiscipline(p.Discipline)
	if err != nil {
		http.Error(w, "Disziplin nicht gefunden", http.StatusNotFound)
		return
	}
	// "Beendet"-Markierung: No editing is permitted – not even for Admin
	// or Scorer. The lock can only be removed in the Admin area; the
	// panel there already hides the input fields.
	if c, err := a.store.GetCompetition(d.CompetitionID); err == nil && c.Finished {
		http.Error(w, "Turnier beendet – keine Änderungen möglich", http.StatusForbidden)
		return
	}

	// Offizielle Gesamtsumme: zählt die (NumJ-2) gestrichenfreien Noten,
	// also max. (NumJ-2)*100 Punkte.
	if _, has := r.Form["total_override"]; has {
		vs := strings.TrimSpace(r.Form.Get("total_override"))
		var ovr *int
		if vs != "" {
			v, err := strconv.Atoi(vs)
			maxTotal := (d.NumJudges - 2) * model.MaxScore
			if maxTotal < 0 {
				maxTotal = model.MaxScore
			}
			if err != nil || v < 0 || v > maxTotal {
				http.Error(w, fmt.Sprintf("Summe muss 0–%d sein", maxTotal), http.StatusBadRequest)
				return
			}
			ovr = &v
		}
		if err := a.store.SetTotalOverride(pid, ovr); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.refresh(w, r, true, false)
		return
	}

	judge, _ := strconv.Atoi(r.Form.Get("judge"))
	valueStr := r.Form.Get("value")
	if pid == 0 || judge < 1 {
		http.Error(w, "ungültige Parameter", http.StatusBadRequest)
		return
	}
	if judge > d.NumJudges {
		http.Error(w, "Richter-Nummer zu hoch", http.StatusBadRequest)
		return
	}
	// Optionaler Client-Zeitstempel „at" (Offline-Outbox): Das Gerät merkt
	// sich den Erfassungszeitpunkt einer Note und schickt ihn beim Nachholen
	// mit. Nur plausible Werte akzeptieren (nicht in der Zukunft, höchstens
	// 24 h alt); sonst zählt die aktuelle Zeit.
	at := time.Now()
	if vs := strings.TrimSpace(r.Form.Get("at")); vs != "" {
		if t, err := time.Parse(time.RFC3339Nano, vs); err == nil {
			if age := time.Since(t); age > -5*time.Minute && age < 24*time.Hour {
				at = t
			}
		}
	}
	if valueStr == "" {
		if err := a.store.ClearScore(pid, judge); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		value, err := strconv.Atoi(valueStr)
		if err != nil || value < 0 || value > model.MaxScore {
			http.Error(w, "Wert muss 0–100 sein", http.StatusBadRequest)
			return
		}
		if err := a.store.SetScoreAt(pid, judge, value, at); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Wird mit dieser Note der Teilnehmer komplett, folgt das Panel
	// wieder dem natürlichen Ablauf: das Cookie (letzte manuelle Wahl)
	// zeigt sonst beim nächsten Polling weiter auf den Fertigen.
	if pp, err := a.store.GetParticipant(pid); err == nil && len(pp.Scores) == d.NumJudges {
		http.SetCookie(w, &http.Cookie{
			Name: selCookieKey, Value: "", Path: "/", MaxAge: -1,
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Antwort: komplette OOB-Updates (Status, Ranglisten, Zeitplan, Panel).
	a.refresh(w, r, true, true)
}

// renderOOB rendert die Partial-Blöcke mit hx-swap-oob. Das Score-Panel wird
// nur mit withPanel neu gerendert (s. refresh).
func (a *App) renderOOB(w http.ResponseWriter, ld *LiveData, withPanel bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeBlock := func(id string, partial string) {
		fmt.Fprintf(w, `<div id="%s" hx-swap-oob="true">`, id)
		a.renderPartial(w, partial, ld)
		w.Write([]byte("</div>"))
	}
	writeBlock("statusbar", "status")
	writeBlock("ranking", "rank")
	writeBlock("schedule", "schedule")
	if withPanel {
		writeBlock("scorepanel", "scorepanel")
	}
	writeBlock("topmeta", "topmeta")
}

func (a *App) renderPartial(w http.ResponseWriter, name string, data any) {
	t := a.tmpl["live"]
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		// Fehler nach Teil-Output nur loggen (OOB-Fragment wäre sonst unvollständig).
		fmt.Fprintf(w, "<!-- Templatefehler %s: %v -->", name, err)
	}
}
