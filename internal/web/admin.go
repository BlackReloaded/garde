package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"turnier/internal/model"
)

// AdminData ist der Datensatz für die Admin-Ansicht.
type AdminData struct {
	Auth     AuthState
	Sessions []model.Session
	Comps    []model.Competition
	Discs    []model.Discipline
	// CompDiscs/CompBlocks: Disziplinen und eingeplante Blöcke pro
	// Turnier der Session (für die Position-Auswahl der Blöcke).
	CompDiscs  map[int64][]model.Discipline
	CompBlocks map[int64][]model.PlannedBlock
	RolesAdm   []model.RoleEntry // effektive Admin-Liste (Flags ∪ UI)
	RolesScr   []model.RoleEntry // effektive Bewertende-Liste (Flags ∪ UI)
	SessionID  int64
	CompID     int64
	DiscID     int64
	Disc       *model.Discipline
	Parts      []model.Participant
	Flash      string
	FlashOK    bool
}

func (a *App) adminData(r *http.Request) *AdminData {
	s, t, d := a.resolveView(r)
	ad := &AdminData{SessionID: s, CompID: t, DiscID: d, Auth: a.auth.authState(r)}
	ad.Sessions, _ = a.store.ListSessions()
	for _, e := range a.auth.ListRoles() {
		if e.Role == model.RoleAdmin {
			ad.RolesAdm = append(ad.RolesAdm, e)
		} else {
			ad.RolesScr = append(ad.RolesScr, e)
		}
	}
	if s != 0 {
		ad.Comps, _ = a.store.ListCompetitions(s)
		ad.CompDiscs = make(map[int64][]model.Discipline)
		ad.CompBlocks = make(map[int64][]model.PlannedBlock)
		for _, c := range ad.Comps {
			if ds, err := a.store.ListDisciplines(c.ID); err == nil {
				ad.CompDiscs[c.ID] = ds
			}
			if bs, err := a.store.ListPlannedBlocks(c.ID); err == nil {
				ad.CompBlocks[c.ID] = bs
			}
		}
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

// adminCompTimes legt die geplante Turnier-Startzeit (HH:MM, leer = nicht
// gesetzt) und die Pause zwischen den Disziplinen (Minuten) fest.
func (a *App) adminCompTimes(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	startAt := 0
	if v := strings.TrimSpace(r.FormValue("start_at")); v != "" {
		tt, err := time.Parse("15:04", v)
		if err != nil {
			a.respondAdmin(w, r, "Ungültige Startzeit (Format HH:MM)", false)
			return
		}
		startAt = tt.Hour()*3600 + tt.Minute()*60
	}
	breakMin := formInt(r, "break_min", 0)
	if breakMin > 240 { // 4 h Pause sind genug
		breakMin = 240
	}
	if err := a.store.UpdateCompetitionTimes(id, startAt, breakMin*60); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Turnier-Zeiten gespeichert", true)
}

// ---------- Geplante Blöcke (Mittagspause, Siegerehrung, …) ----------

// blockFormValide liest und validiert die Block-Formularfelder (Name,
// Position, Dauer in Minuten).
func blockFormVals(r *http.Request) (name string, position, durMin int, ok bool) {
	name = strings.TrimSpace(r.FormValue("name"))
	position, err := strconv.Atoi(strings.TrimSpace(r.FormValue("position")))
	if err != nil {
		position = -1
	}
	durMin = formInt(r, "duration_min", 0)
	if name == "" || position < -1 || durMin < 1 {
		return name, position, durMin, false
	}
	if durMin > 480 { // 8 h Block sind genug
		durMin = 480
	}
	return name, position, durMin, true
}

func (a *App) adminBlockCreate(w http.ResponseWriter, r *http.Request) {
	compID := parseID(r.FormValue("comp_id"))
	if compID == 0 {
		compID = a.adminData(r).CompID
	}
	name, position, durMin, ok := blockFormVals(r)
	if compID == 0 {
		a.respondAdmin(w, r, "Bitte zuerst ein Turnier anlegen", false)
		return
	}
	if !ok {
		a.respondAdmin(w, r, "Name, Position und Dauer (mindestens 1 Minute) sind Pflicht", false)
		return
	}
	// Position jenseits der Disziplinanzahl = am Ende.
	if discs, err := a.store.ListDisciplines(compID); err == nil && position > len(discs) {
		position = -1
	}
	if _, err := a.store.CreatePlannedBlock(model.PlannedBlock{
		CompetitionID: compID, Name: name, Position: position, DurationSec: durMin * 60,
	}); err != nil {
		a.respondAdmin(w, r, "Anlegen fehlgeschlagen: "+err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Block „"+name+"“ eingeplant", true)
}

func (a *App) adminBlockUpdate(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	b, err := a.store.GetPlannedBlock(id)
	if err != nil {
		a.respondAdmin(w, r, "Block nicht gefunden", false)
		return
	}
	name, position, durMin, ok := blockFormVals(r)
	if !ok {
		a.respondAdmin(w, r, "Name, Position und Dauer (mindestens 1 Minute) sind Pflicht", false)
		return
	}
	if discs, err := a.store.ListDisciplines(b.CompetitionID); err == nil && position > len(discs) {
		position = -1
	}
	b.Name, b.Position, b.DurationSec = name, position, durMin*60
	if err := a.store.UpdatePlannedBlock(*b); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Block gespeichert", true)
}

func (a *App) adminBlockDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.FormValue("id"))
	if id == 0 {
		a.respondAdmin(w, r, "id fehlt", false)
		return
	}
	if err := a.store.DeletePlannedBlock(id); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, "Block entfernt", true)
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
	w.Header().Set("Content-Disposition", `attachment; filename="turnier-export.csv"`)
	w.Write([]byte(text))
}

// ---------- Berechtigungen (Rollen-Listen) ----------

// adminRoleAdd ergänzt eine E-Mail-Adresse als admin bzw. bewerten.
// Start-Flags bleiben unberührt; der Eintrag wirkt sofort (DB).
func (a *App) adminRoleAdd(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	role := r.FormValue("role")
	if err := a.auth.AddRole(email, role); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, fmt.Sprintf("%s als %s eingetragen – wirkt sofort",
		email, RoleLabel(role)), true)
}

// adminRoleRemove entfernt einen UI-Eintrag (Start-Flags sind nicht
// entfernbar). Wirkt sofort: die Rolle wird beim nächsten Request neu
// geprüft.
func (a *App) adminRoleRemove(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	role := r.FormValue("role")
	if err := a.auth.RemoveRole(email, role); err != nil {
		a.respondAdmin(w, r, err.Error(), false)
		return
	}
	a.respondAdmin(w, r, fmt.Sprintf("%s aus %s entfernt",
		email, RoleLabel(role)), true)
}
