package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tuniert/internal/model"
)

// AdminData ist der Datensatz für die Admin-Ansicht.
type AdminData struct {
	Sessions  []model.Session
	Comps     []model.Competition
	Discs     []model.Discipline
	SessionID int64
	CompID    int64
	DiscID    int64
	Disc      *model.Discipline
	Parts     []model.Participant
	Flash     string
	FlashOK   bool
}

func (a *App) adminData(r *http.Request) *AdminData {
	s, t, d := a.resolveView(r)
	ad := &AdminData{SessionID: s, CompID: t, DiscID: d}
	ad.Sessions, _ = a.store.ListSessions()
	if s != 0 {
		ad.Comps, _ = a.store.ListCompetitions(s)
	}
	if t != 0 {
		ad.Discs, _ = a.store.ListDisciplines(t)
		if d != 0 {
			ad.Disc, _ = a.store.GetDisciplineFull(d)
			ad.Parts, _ = a.store.ListParticipants(d)
		}
	}
	return ad
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, ad *AdminData) {
	a.render(w, r, http.StatusOK, "admin", ad)
}

// respondAdmin rendert die Admin-Seite neu, optional mit Flash-Nachricht
// und optional umgeschalteter Ansicht (q = neuer RawQuery).
func (a *App) respondAdmin(w http.ResponseWriter, r *http.Request, flash string, ok bool, q ...string) {
	if len(q) > 0 && q[0] != "" {
		r.URL.RawQuery = q[0]
	}
	ad := a.adminData(r)
	ad.Flash, ad.FlashOK = flash, ok
	a.renderAdmin(w, r, ad)
}

func (a *App) adminPage(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, a.adminData(r))
}

// ---------- Session ----------

func (a *App) adminSessionCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		a.respondAdmin(w, r, "Name ist Pflicht", false)
		return
	}
	id, err := a.store.CreateSession(name)
	if err != nil {
		a.respondAdmin(w, r, "Anlegen fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Session angelegt", true, "s="+strconv.FormatInt(id, 10))
}

func (a *App) adminSessionRename(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if id == 0 || name == "" {
		a.respondAdmin(w, r, "id und name sind Pflicht", false)
		return
	}
	if err := a.store.RenameSession(id, name); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Session umbenannt", true)
}

func (a *App) adminSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	if err := a.store.DeleteSession(id); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Session (inkl. Turniere, Disziplinen, Teilnehmer, Noten) gelöscht", true)
}

// ---------- Turnier ----------

func (a *App) adminCompCreate(w http.ResponseWriter, r *http.Request) {
	sessionID := parseID(r.FormValue("session_id"))
	if sessionID == 0 {
		sessionID = a.adminData(r).SessionID
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if sessionID == 0 || name == "" {
		a.respondAdmin(w, r, "Name ist Pflicht", false)
		return
	}
	id, err := a.store.CreateCompetition(sessionID, name)
	if err != nil {
		a.respondAdmin(w, r, "Anlegen fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Turnier angelegt", true,
		fmt.Sprintf("s=%d&t=%d", sessionID, id))
}

func (a *App) adminCompRename(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if id == 0 || name == "" {
		a.respondAdmin(w, r, "id und name sind Pflicht", false)
		return
	}
	if err := a.store.RenameCompetition(id, name); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Turnier umbenannt", true)
}

func (a *App) adminCompDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	if err := a.store.DeleteCompetition(id); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Turnier (inkl. Disziplinen & Teilnehmer) gelöscht", true)
}

// ---------- Disziplin ----------

func (a *App) adminDiscCreate(w http.ResponseWriter, r *http.Request) {
	compID := parseID(r.FormValue("comp_id"))
	if compID == 0 {
		compID = a.adminData(r).CompID
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if compID == 0 || name == "" {
		a.respondAdmin(w, r, "Name ist Pflicht", false)
		return
	}
	id, err := a.store.CreateDiscipline(compID, model.Discipline{Name: name})
	if err != nil {
		a.respondAdmin(w, r, "Anlegen fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Disziplin angelegt", true,
		fmt.Sprintf("t=%d&d=%d", compID, id))
}

func (a *App) adminDiscUpdate(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	d, err := a.store.GetDiscipline(id)
	if err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	d.Name = strings.TrimSpace(r.FormValue("name"))
	d.MaxDanceSec = formInt(r, "max_dance_sec", d.MaxDanceSec)
	d.ScoringDefSec = formInt(r, "scoring_def_sec", d.ScoringDefSec)
	d.WalkOnSec = formInt(r, "walk_on_sec", d.WalkOnSec)
	d.NumJudges = formInt(r, "num_judges", d.NumJudges)
	if d.Name == "" {
		a.respondAdmin(w, r, "Name ist Pflicht", false)
		return
	}
	if err := a.store.UpdateDiscipline(*d); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Disziplin gespeichert", true)
}

func (a *App) adminDiscDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	if err := a.store.DeleteDiscipline(id); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Disziplin (inkl. Teilnehmer & Noten) gelöscht", true)
}

func formInt(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(key)))
	if err != nil || v < 0 {
		return def
	}
	return v
}

// ---------- Teilnehmer ----------

func (a *App) adminPartCreate(w http.ResponseWriter, r *http.Request) {
	discID := parseID(r.FormValue("disc_id"))
	if discID == 0 {
		discID = a.adminData(r).DiscID
	}
	p := model.Participant{
		Discipline: discID,
		StartNr:    strings.TrimSpace(r.FormValue("start_nr")),
		Name:       strings.TrimSpace(r.FormValue("name")),
		Verein:     strings.TrimSpace(r.FormValue("verein")),
		Starred:    r.FormValue("starred") == "on",
	}
	if p.StartNr == "" || p.Name == "" {
		a.respondAdmin(w, r, "Startnummer und Name sind Pflicht", false)
		return
	}
	if discID == 0 {
		a.respondAdmin(w, r, "Bitte zuerst eine Disziplin anlegen", false)
		return
	}
	if _, err := a.store.CreateParticipant(p); err != nil {
		a.respondAdmin(w, r, "Anlegen fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Teilnehmer angelegt", true, "d="+strconv.FormatInt(discID, 10))
}

func (a *App) adminPartUpdate(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	p, err := a.store.GetParticipant(id)
	if err != nil {
		a.respondAdmin(w, r, "Teilnehmer nicht gefunden", false)
		return
	}
	p.StartNr = strings.TrimSpace(r.FormValue("start_nr"))
	p.Name = strings.TrimSpace(r.FormValue("name"))
	p.Verein = strings.TrimSpace(r.FormValue("verein"))
	p.Starred = r.FormValue("starred") == "on"
	p.Withdrawn = r.FormValue("withdrawn") == "on"
	if p.StartNr == "" || p.Name == "" {
		a.respondAdmin(w, r, "Startnummer und Name sind Pflicht", false)
		return
	}
	if err := a.store.UpdateParticipant(*p); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Teilnehmer gespeichert", true)
}

func (a *App) adminPartMove(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	dir := 1
	if r.FormValue("dir") == "-1" {
		dir = -1
	}
	if err := a.store.MoveParticipant(id, dir); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "", true)
}

func (a *App) adminPartDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if err := a.store.DeleteParticipant(id); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Teilnehmer gelöscht", true)
}

// ---------- Noten / CSV ----------

func (a *App) adminScoresReset(w http.ResponseWriter, r *http.Request) {
	discID := parseID(r.FormValue("disc_id"))
	if discID == 0 {
		discID = a.adminData(r).DiscID
	}
	if discID == 0 {
		a.respondAdmin(w, r, "keine Disziplin ausgewählt", false)
		return
	}
	if err := a.store.ResetScores(discID); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Alle Noten der Disziplin gelöscht", true, "d="+strconv.FormatInt(discID, 10))
}

func (a *App) adminCSVImport(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.FormValue("csv"))
	if text == "" {
		a.respondAdmin(w, r, "Kein CSV-Inhalt angegeben", false)
		return
	}
	n, err := a.store.ImportCSV(text)
	if err != nil {
		a.respondAdmin(w, r, "Import fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, fmt.Sprintf("Import: %d Zeilen übernommen", n), true)
}

func (a *App) adminCSVExport(w http.ResponseWriter, r *http.Request) {
	sessionID := parseID(r.URL.Query().Get("s"))
	if sessionID == 0 {
		sessionID = a.adminData(r).SessionID
	}
	text, err := a.store.ExportCSV(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tuniert-export.csv"`)
	w.Write([]byte(text))
}
