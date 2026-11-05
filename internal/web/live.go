package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"tuniert/internal/model"
)

// ViewParamKey ist der Cookie-Name für die zuletzt gewählte Ansicht.
const ViewParamKey = "tuniert_view"

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
const selCookieKey = "tuniert_sel"

// explicitSel liest einen expliziten sel-Wert (URL oder Formular).
func explicitSel(r *http.Request) int64 {
	if s := parseID(r.URL.Query().Get("sel")); s != 0 {
		return s
	}
	return parseID(r.FormValue("sel"))
}

// panelSelection bestimmt den im Panel aktiven Teilnehmer:
// explizites sel > Cookie > gerade an der Reihe.
func (a *App) panelSelection(r *http.Request, ld *LiveData) int64 {
	sel := explicitSel(r)
	if sel == 0 {
		if c, err := r.Cookie(selCookieKey); err == nil {
			sel = parseID(c.Value)
		}
	}
	if sel != 0 {
		for i := range ld.Active {
			p := ld.Active[i]
			if p.ID == sel && len(p.Scores) < ld.NumJ {
				return p.ID
			}
		}
	}
	if ld.Sched != nil && ld.Sched.Current != nil {
		return ld.Sched.Current.ID
	}
	return 0
}

func (a *App) maybeSetSelCookie(w http.ResponseWriter, r *http.Request) {
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
	a.finalizePanel(ld, a.panelSelection(r, ld))
	a.render(w, r, http.StatusOK, "live", ld)
}

// refresh liefert OOB-Partial-Updates für die Live-Ansicht (Polling + nach Score).
func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
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
	a.finalizePanel(ld, a.panelSelection(r, ld))
	a.renderOOB(w, ld)
}

// score übernimmt eine Einzelnote (value leer = löschen).
func (a *App) score(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "ungültiges Formular", http.StatusBadRequest)
		return
	}
	pid := parseID(r.Form.Get("participant_id"))
	judge, _ := strconv.Atoi(r.Form.Get("judge"))
	valueStr := r.Form.Get("value")
	if pid == 0 || judge < 1 {
		http.Error(w, "ungültige Parameter", http.StatusBadRequest)
		return
	}
	p, err := a.store.GetParticipant(pid)
	if err != nil {
		http.Error(w, "Teilnehmer nicht gefunden", http.StatusNotFound)
		return
	}
	// Disziplin nur zur Plausibilitätsprüfung (Richteranzahl).
	d, err := a.store.GetDiscipline(p.Discipline)
	if err != nil {
		http.Error(w, "Disziplin nicht gefunden", http.StatusNotFound)
		return
	}
	if judge > d.NumJudges {
		http.Error(w, "Richter-Nummer zu hoch", http.StatusBadRequest)
		return
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
		if err := a.store.SetScore(pid, judge, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Antwort: komplette OOB-Updates (Status, Ranglisten, Zeitplan, Panel).
	a.refresh(w, r)
}

// renderOOB rendert die vier Partial-Blöcke mit hx-swap-oob.
func (a *App) renderOOB(w http.ResponseWriter, ld *LiveData) {
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
	writeBlock("scorepanel", "scorepanel")
	writeBlock("topmeta", "topmeta")
}

func (a *App) renderPartial(w http.ResponseWriter, name string, data any) {
	t := a.tmpl["live"]
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		// Fehler nach Teil-Output nur loggen (OOB-Fragment wäre sonst unvollständig).
		fmt.Fprintf(w, "<!-- Templatefehler %s: %v -->", name, err)
	}
}
