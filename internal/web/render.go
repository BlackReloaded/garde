package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"turnier/internal/model"
	"turnier/internal/schedule"
)

// funcs sind die Template-Hilfsfunktionen.
var funcs = template.FuncMap{
	"star": func(b bool) string {
		if b {
			return "★"
		}
		return ""
	},
	"seq": func(n int) []int {
		out := make([]int, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, i)
		}
		return out
	},
	"join": strings.Join,
	"add":  func(a, b int) int { return a + b },
	"div": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a / b
	},
	// coalesce liefert den ersten nicht-leeren String (z. B. Name, sonst
	// E-Mail). Der Name darf NICHT "or" sein: Das würde das eingebaute
	// Template-Funktions-or (Boolean OR) überschreiben und z. B.
	// "or (eq 1 1) (eq 2 2)" in der Rangliste zum Absturz bringen.
	"coalesce": func(a, b string) string {
		if strings.TrimSpace(a) == "" {
			return b
		}
		return a
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[fmt.Sprint(kv[i])] = kv[i+1]
		}
		return m
	},
	// startLabel rendert den Slot-Start der aktuellen Reihe: konkrete
	// (geplante) Startzeit, wenn sie (mindestens 1 min) in der Zukunft
	// liegt, sonst "start ≈ jetzt".
	"startLabel": func(startEst *time.Time, now time.Time) string {
		if startEst != nil && startEst.After(now.Add(time.Minute)) {
			return "start ca. " + startEst.In(schedule.Zone).Format("15:04")
		}
		return "start ≈ jetzt"
	},
}

// fmtHM/fmtHMS zeigen Zeiten immer in europäischer Zeit (Europe/Berlin) –
// die Zeitwerte selbst dürfen in beliebiger Zone vorliegen (UTC im Container).
func fmtHM(t *time.Time) string {
	if t == nil {
		return "–"
	}
	return t.In(schedule.Zone).Format("15:04")
}

func fmtHMS(t *time.Time) string {
	if t == nil {
		return "–"
	}
	return t.In(schedule.Zone).Format("15:04:05")
}

// fmtDur rendert Sekunden als "mm:ss" bzw. "h:mm:ss".
func fmtDur(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// fmtTimeInput rendert Sekunden-seit-Mitternacht als "HH:MM"
// (für <input type="time"> in der Admin-UI).
func fmtTimeInput(sec int) string {
	return fmt.Sprintf("%02d:%02d", sec/3600, (sec%3600)/60)
}

func init() {
	funcs["fmtHM"] = fmtHM
	funcs["fmtHMS"] = fmtHMS
	funcs["fmtDur"] = fmtDur
	funcs["fmtTimeInput"] = fmtTimeInput
	funcs["pct"] = func(a, b int) int {
		if b <= 0 {
			return 0
		}
		return a * 100 / b
	}
	funcs["sub"] = func(a, b int) int { return a - b }
}

// RankRow ist eine Zeile der Rangliste.
type RankRow struct {
	P        model.Participant
	Values   []int // Noten nach Richter (1..n), fehlende = -1
	LowIdx   int   // Index (0-basiert) der gestrichenen Mindestnote (-1 bei unvollständig)
	HighIdx  int
	Total    int
	Rank     int // 0 = keine (in Bewertung)
	Complete bool
	N        int
	// Official = true, wenn die Summe offiziell überschrieben wurde.
	Official bool
}

// SchedRow ist eine Zeile der Zeitplan-Tabelle: ein Teilnehmer mit seiner
// Zeitprognose. Zurückgezogene Teilnehmer sind dabei (als gestrichen
// angezeigt), haben aber keine Zeitangaben – die Zeitprognose springt sie
// über.
type SchedRow struct {
	schedule.ResultEntry
	Withdrawn bool
}

// LiveData ist das Gesamtdatensatz für die Live-Ansicht.
type LiveData struct {
	// Auth ist der Anmeldezustand (Login-Button, Score-Panel-Guard).
	Auth AuthState

	Sessions []model.Session
	Comps    []model.Competition
	Discs    []model.Discipline

	SessionID int64
	CompID    int64
	DiscID    int64
	Disc      *model.Discipline

	Parts     []model.Participant // alle, inkl. zurückgezogenen
	Active    []model.Participant // ohne zurückgezogene, in Startreihenfolge
	Sched     *schedule.Result    // Prognose (nur aktive) für Status/Block-Ende
	SchedRows []SchedRow          // Zeitplan: alle in Startreihenfolge, gestrichene ohne Zeit
	Plain     []RankRow           // Rangliste ohne Stern
	Star      []RankRow           // Rangliste mit Stern
	Pending   []RankRow           // in Bewertung (grau, n/7)
	NumJ      int

	LastScoreAt *time.Time
	Finished    bool
	NowUnix     int64 // für die Uhr
	SlotLabel   string

	// Turnier-Planung (Wettkampf-Ebene): Startzeit + eingeplante Blöcke
	// (Mittagspause, Siegerehrung, …) → geplanter Ablauf (Anzeige + Anker
	// der Prognose, solange noch keine Note existiert). Zwischen den
	// Abschnitten gibt es keine feste Pause mehr.
	CompStart *time.Time             // geplante Turnier-Startzeit, nil = nicht gesetzt
	PlanSegs  []schedule.PlanSegment // geplanter Ablauf (Disziplinen + Blöcke)
	NextSeg   *schedule.PlanSegment  // nächster Abschnitt im Plan (nil = keiner sichtbar)

	// PanelPID ist der im Punkteeingabe-Panel ausgewählte Teilnehmer
	// (Default: gerade an der Reihe; für Korrekturen wählbar).
	PanelPID int64

	// Zählwerte für die Header der Ranglisten.
	ActivePlain int
	ActiveStar  int

	// PanelValues: Noten des Panel-Teilnehmers nach Richter (fehlend = -1).
	PanelValues []int
	PanelN      int
	PanelPart   *model.Participant
	// PanelComputed = aus den Noten berechnete Summe (0 wenn unvollständig);
	// PanelOverride = offiziell genannte Summe (nil = keine Überschreibung).
	PanelComputed int
	PanelOverride *int
}

// render rendert eine vollständige Template-Seite (Seite = "live" oder "admin";
// das Page-Template definiert "body", base.html liefert das Gerüst).
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	t := a.tmpl[page]
	if t == nil {
		http.Error(w, "unbekannte Seite", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("Template %s: %v", page, err)
	}
}

// fullRank ist ein kompletter Ranglisteneintrag vor der Platzierungs-Zuweisung.
type fullRank struct {
	row      RankRow
	total    int
	override *int
}

// loadLive baut den LiveData für eine Disziplin.
func (a *App) loadLive(discID int64, now time.Time) (*LiveData, error) {
	d, err := a.store.GetDisciplineFull(discID)
	if err != nil {
		return nil, err
	}
	sessions, err := a.store.ListSessions()
	if err != nil {
		return nil, err
	}
	// Das Turnier der Disziplin (Session-ID + geplante Startzeit).
	comp, err := a.store.GetCompetition(d.CompetitionID)
	if err != nil {
		return nil, err
	}
	comps, err := a.store.ListCompetitions(comp.SessionID)
	if err != nil {
		return nil, err
	}
	parts, err := a.store.ListParticipantsWithScores(d.ID)
	if err != nil {
		return nil, err
	}

	ld := &LiveData{
		Sessions:  sessions,
		Comps:     comps,
		Discs:     []model.Discipline{},
		CompID:    d.CompetitionID,
		DiscID:    d.ID,
		Disc:      d,
		Parts:     parts,
		NumJ:      d.NumJudges,
		NowUnix:   now.Unix(),
		SessionID: comp.SessionID,
	}
	// Alle Disziplinen des Turniers (für den Selektor).
	allDiscs, err := a.store.ListDisciplines(d.CompetitionID)
	if err != nil {
		return nil, err
	}
	ld.Discs = allDiscs

	// Turnier-Planung: Ablauf aus Turnier-Startzeit + Disziplin-Dauern
	// (Teilnehmer × Slot-Default) + eingeplanten Blöcken (Mittagspause,
	// Siegerehrung, …). Die Kette wird an den gemessenen Noten-Zeitstempeln
	// verankert (Realität schlägt Plan), siehe schedule.PlanTimeline.
	// Dient der Anzeige (Turnier-Start, Disziplin-Fahrplan, „als Nächstes")
	// und als Anker der Prognose der aktuellen Disziplin, solange dort noch
	// keine Note existiert.
	var anchorAt *time.Time
	blocks, err := a.store.ListPlannedBlocks(d.CompetitionID)
	if err != nil {
		return nil, err
	}
	if comp != nil && (comp.StartAtSec > 0 || len(blocks) > 0) {
		// Messbasis: Zeitstempel aller Disziplinen des Turniers (für die
		// aktuelle die bereits geladenen Teilnehmer weiterverwenden).
		act := make(map[int64]schedule.DiscActual, len(allDiscs))
		for _, dd := range allDiscs {
			ps := parts
			if dd.ID != d.ID {
				ps, err = a.store.ListParticipantsWithScores(dd.ID)
				if err != nil {
					return nil, err
				}
			}
			act[dd.ID] = schedule.ActualFromParticipants(dd.NumJudges, ps)
		}
		plan := schedule.PlanTimeline(comp.StartAtSec, now, schedule.Zone, allDiscs, blocks, act)
		ld.PlanSegs = plan
		if comp.StartAtSec > 0 {
			start := schedule.PlanStartAt(comp.StartAtSec, now, schedule.Zone)
			ld.CompStart = &start
		}
		// Anker nur aus der aktuellen Disziplin (nicht aus einem
		// Block mit zufällig gleicher ID); ihre Position bestimmt,
		// ab wo „als Nächstes“ gesucht wird.
		curIdx := -1
		for i := range plan {
			if plan[i].Kind == schedule.SegmentDiscipline && plan[i].ID == d.ID {
				anchorAt = plan[i].StartEst
				curIdx = i
				break
			}
		}
		// Als Nächstes: erster Abschnitt in der Zukunft – nach der
		// aktuellen Disziplin im Plan (sonst: überhaupt).
		from := 0
		if curIdx >= 0 {
			from = curIdx + 1
		}
		for i := from; i < len(plan); i++ {
			if plan[i].StartEst.After(now) {
				ld.NextSeg = &plan[i]
				break
			}
		}
	}

	// Aktive Teilnehmer (ohne zurückgezogene) in Startreihenfolge: maßgeblich
	// ist die Import-/Editor-Reihenfolge (sort_order), NICHT die
	// Startnummer – echte Startlisten führen Nachmeldungen ("00x") gern
	// absteigend auf (002 startet vor 001); die Auslosungsreihenfolge ist
	// die Listenreihenfolge.
	activeParts := make([]model.Participant, 0, len(parts))
	for _, p := range parts {
		if p.Withdrawn {
			continue
		}
		activeParts = append(activeParts, p)
	}
	infos := make([]model.ParticipantInfo, 0, len(activeParts))
	for _, p := range activeParts {
		ld.Active = append(ld.Active, p)
		sa := make([]model.ScoreAt, 0, len(p.Scores))
		for _, s := range p.Scores {
			sa = append(sa, model.ScoreAt{Value: s.Value, At: s.UpdatedAt})
		}
		infos = append(infos, model.ParticipantInfo{
			ID: p.ID, StartNr: p.StartNr, Name: p.Name,
			Verein: p.Verein, Starred: p.Starred, Scores: sa,
		})
	}

	schedResult := schedule.Compute(schedule.Settings{
		MaxDanceSec:   d.MaxDanceSec,
		ScoringDefSec: d.ScoringDefSec,
		WalkOnSec:     d.WalkOnSec,
		NumJudges:     d.NumJudges,
		AnchorAt:      anchorAt,
	}, infos, now)
	ld.Sched = &schedResult
	ld.Finished = ld.Sched.Finished
	ld.SlotLabel = fmtDur(ld.Sched.SlotSec)

	// Zeitplan-Tabelle: nur die noch ausstehenden Tänze, in
	// Startreihenfolge (= Listenreihenfolge, s. oben). Was schon Noten
	// hat, ist bereits in der "In Bewertung"-Karte (1–6 Noten) bzw. in
	// der Rangliste (7/7) – das muss hier nicht doppelt stehen.
	// Zurückgezogene erscheinen gestrichen, solange die Liste bei ihnen
	// steht; sind sie gelaufen (Position vor dem aktuellen Tänzer), fallen
	// auch sie weg. Die Zeitprognose überspringt sie ohnehin (kein Slot,
	// keine Zeit) – daher per ID mit den Prognose-Einträgen matchen.
	prognose := make(map[int64]schedule.ResultEntry, len(schedResult.Entries))
	for _, e := range schedResult.Entries {
		prognose[e.ID] = e
	}
	// Position des aktuellen Tanzers in der Liste (=-1 wenn nichts mehr
	// läuft): maßgeblich dafür, welche zurückgezogenen Einträge noch
	// "vorn" liegen.
	curPos := -1
	if c := schedResult.Current; c != nil {
		for i := range parts {
			if parts[i].ID == c.ID {
				curPos = i
				break
			}
		}
	}
	rows := make([]SchedRow, 0, len(parts))
	for i, p := range parts {
		if p.Withdrawn {
			if curPos < 0 || i < curPos {
				continue
			}
			rows = append(rows, SchedRow{
				ResultEntry: schedule.ResultEntry{
					ID: p.ID, StartNr: p.StartNr, Name: p.Name,
					Verein: p.Verein, Starred: p.Starred,
					State: model.StateWaiting,
				},
				Withdrawn: true,
			})
			continue
		}
		e, ok := prognose[p.ID]
		if !ok || e.NumScores > 0 {
			continue // schon in Bewertung oder fertig
		}
		rows = append(rows, SchedRow{ResultEntry: e})
	}
	ld.SchedRows = rows

	if t, ok, _ := a.store.LastScoreTime(d.ID); ok {
		ld.LastScoreAt = &t
	}

	// Ranglisten.
	var plain, star []fullRank
	for _, p := range ld.Active {
		if p.Starred {
			ld.ActiveStar++
		} else {
			ld.ActivePlain++
		}
	}
	for _, p := range ld.Active {
		values, lo, hi, complete := model.ScoresByJudge(p.Scores, d.NumJudges)
		row := RankRow{
			P:        p,
			Values:   make([]int, d.NumJudges),
			LowIdx:   -1,
			HighIdx:  -1,
			Complete: complete,
			N:        len(p.Scores),
		}
		for j := 0; j < d.NumJudges; j++ {
			row.Values[j] = -1
		}
		// Werte nach Richter ordnen.
		byJudge := map[int]int{}
		for _, s := range p.Scores {
			if s.Judge >= 1 && s.Judge <= d.NumJudges {
				byJudge[s.Judge] = s.Value
			}
		}
		for j := 1; j <= d.NumJudges; j++ {
			if v, ok := byJudge[j]; ok {
				row.Values[j-1] = v
			}
		}
		if complete {
			total, _ := model.TotalOf(values, d.NumJudges)
			row.Total = total
			row.LowIdx, row.HighIdx = lo, hi
			// Offiziell genannte Summe gewinnt über die berechnete.
			if p.TotalOverride != nil {
				row.Total = *p.TotalOverride
				row.Official = true
			}
			f := fullRank{row: row, total: row.Total, override: p.TotalOverride}
			if p.Starred {
				star = append(star, f)
			} else {
				plain = append(plain, f)
			}
		} else if len(p.Scores) > 0 {
			// Nur echte Teildwertungen (1–6 Noten) sind "in Bewertung";
			// Tänzer ohne jede Note stehen (noch) im Zeitplan.
			ld.Pending = append(ld.Pending, row)
		}
	}

	ld.Plain = a.rankFulls(plain, d.NumJudges)
	ld.Star = a.rankFulls(star, d.NumJudges)
	// Pending entsteht in ld.Active-Reihenfolge (Startreihenfolge) – keine
	// Umsortierung.
	return ld, nil
}

// finalizePanel füllt die Panel-Daten für den ausgewählten Teilnehmer.
func (a *App) finalizePanel(ld *LiveData, pid int64) {
	ld.PanelPID = pid
	ld.PanelValues = make([]int, ld.NumJ)
	for i := range ld.PanelValues {
		ld.PanelValues[i] = -1
	}
	for i := range ld.Active {
		p := &ld.Active[i]
		if p.ID != pid {
			continue
		}
		ld.PanelPart = p
		ld.PanelN = len(p.Scores)
		for _, s := range p.Scores {
			if s.Judge >= 1 && s.Judge <= ld.NumJ {
				ld.PanelValues[s.Judge-1] = s.Value
			}
		}
		ld.PanelOverride = p.TotalOverride
		if len(p.Scores) == ld.NumJ {
			vals := make([]int, 0, ld.NumJ)
			for _, v := range ld.PanelValues {
				if v >= 0 {
					vals = append(vals, v)
				}
			}
			ld.PanelComputed, _ = model.TotalOf(vals, ld.NumJ)
		}
		break
	}
}

func (a *App) rankFulls(fs []fullRank, numJ int) []RankRow {
	entries := make([]model.RankEntry, 0, len(fs))
	for _, f := range fs {
		scores := make([]int, 0, numJ)
		for _, v := range f.row.Values {
			if v >= 0 {
				scores = append(scores, v)
			}
		}
		entries = append(entries, model.RankEntry{
			ID: f.row.P.ID, Scores: scores, Starred: f.row.P.Starred,
			Override: f.override,
		})
	}
	ranked := model.Rank(entries)
	out := make([]RankRow, 0, len(ranked))
	posOfID := map[int64]int{}
	for i, f := range fs {
		posOfID[f.row.P.ID] = i
	}
	for _, r := range ranked {
		f := fs[posOfID[r.Entry.ID]]
		f.row.Total = r.Total
		f.row.Rank = r.Rank
		out = append(out, f.row)
	}
	return out
}
