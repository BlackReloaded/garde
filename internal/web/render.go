package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"tuniert/internal/model"
	"tuniert/internal/schedule"
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
}

func fmtHM(t *time.Time) string {
	if t == nil {
		return "–"
	}
	return t.Format("15:04")
}

func fmtHMS(t *time.Time) string {
	if t == nil {
		return "–"
	}
	return t.Format("15:04:05")
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

func init() {
	funcs["fmtHM"] = fmtHM
	funcs["fmtHMS"] = fmtHMS
	funcs["fmtDur"] = fmtDur
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
	comps, err := a.store.ListCompetitions(d.CompetitionID)
	if err != nil {
		return nil, err
	}
	parts, err := a.store.ListParticipantsWithScores(d.ID)
	if err != nil {
		return nil, err
	}

	ld := &LiveData{
		Sessions: sessions,
		Comps:    comps,
		Discs:    []model.Discipline{},
		CompID:   d.CompetitionID,
		DiscID:   d.ID,
		Disc:     d,
		Parts:    parts,
		NumJ:     d.NumJudges,
		NowUnix:  now.Unix(),
	}
	// Session-ID des Turniers.
	if c, err := a.store.GetCompetition(d.CompetitionID); err == nil {
		ld.SessionID = c.SessionID
	}
	// Alle Disziplinen des Turniers (für den Selektor).
	allDiscs, err := a.store.ListDisciplines(d.CompetitionID)
	if err != nil {
		return nil, err
	}
	ld.Discs = allDiscs

	// Aktive Teilnehmer (ohne zurückgezogene) in Startreihenfolge:
	// null-padding-Nummern ("001" …) zuerst, dann die übrigen numerisch.
	activeParts := make([]model.Participant, 0, len(parts))
	for _, p := range parts {
		if p.Withdrawn {
			continue
		}
		activeParts = append(activeParts, p)
	}
	sort.SliceStable(activeParts, func(i, j int) bool {
		return startNrLess(activeParts[i], activeParts[j])
	})
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
	}, infos, now)
	ld.Sched = &schedResult
	ld.Finished = ld.Sched.Finished
	ld.SlotLabel = fmtDur(ld.Sched.SlotSec)

	// Zeitplan-Tabelle: ALLE Teilnehmer (auch zurückgezogene) in
	// Startreihenfolge. Zurückgezogene erscheinen gestrichen, werden aber
	// von der Zeitprognose übersprungen (kein Slot, keine Zeit) – daher
	// per ID mit den Prognose-Einträgen (nur aktive) matchen.
	allSorted := append([]model.Participant(nil), parts...)
	sort.SliceStable(allSorted, func(i, j int) bool {
		return startNrLess(allSorted[i], allSorted[j])
	})
	prognose := make(map[int64]schedule.ResultEntry, len(schedResult.Entries))
	for _, e := range schedResult.Entries {
		prognose[e.ID] = e
	}
	rows := make([]SchedRow, 0, len(allSorted))
	for _, p := range allSorted {
		if e, ok := prognose[p.ID]; ok {
			rows = append(rows, SchedRow{ResultEntry: e})
		} else {
			rows = append(rows, SchedRow{
				ResultEntry: schedule.ResultEntry{
					ID: p.ID, StartNr: p.StartNr, Name: p.Name,
					Verein: p.Verein, Starred: p.Starred,
					State: model.StateWaiting,
				},
				Withdrawn: true,
			})
		}
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
		} else {
			ld.Pending = append(ld.Pending, row)
		}
	}

	ld.Plain = a.rankFulls(plain, d.NumJudges)
	ld.Star = a.rankFulls(star, d.NumJudges)
	// Pending in Startreihenfolge (wie ld.Active).
	sort.SliceStable(ld.Pending, func(i, j int) bool {
		return startNrLess(ld.Pending[i].P, ld.Pending[j].P)
	})
	return ld, nil
}

// startNrClass ordnet Startnummern in drei Gruppen:
// 0 = null-padding ("001" …) – dieser Block steht immer zuerst,
// 1 = rein numerisch, 2 = nicht-numerisch. num ist der Zahlenwert.
func startNrClass(s string) (class int, num int64) {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 2, 0
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, n
	}
	return 1, n
}

// startNrLess vergleicht Teilnehmer nach Startnummer: zuerst der
// null-padding-Block ("001", "002", …), dann die übrigen numerisch
// aufsteigend, zuletzt nicht-numerische (alphabetisch); bei
// Gleichstand innerhalb einer Gruppe entscheidet die ID.
func startNrLess(a, b model.Participant) bool {
	ga, na := startNrClass(a.StartNr)
	gb, nb := startNrClass(b.StartNr)
	switch {
	case ga != gb:
		return ga < gb
	case ga < 2 && na != nb:
		return na < nb
	case strings.TrimSpace(a.StartNr) != strings.TrimSpace(b.StartNr):
		return strings.TrimSpace(a.StartNr) < strings.TrimSpace(b.StartNr)
	}
	return a.ID < b.ID
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
