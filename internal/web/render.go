package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
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
}

// LiveData ist das Gesamtdatensatz für die Live-Ansicht.
type LiveData struct {
	Sessions []model.Session
	Comps    []model.Competition
	Discs    []model.Discipline

	SessionID int64
	CompID    int64
	DiscID    int64
	Disc      *model.Discipline

	Parts   []model.Participant // alle, inkl. zurückgezogenen (für Ausblendung)
	Active  []model.Participant // ohne zurückgezogene, in Startreihenfolge
	Sched   *schedule.Result
	Plain   []RankRow // Rangliste ohne Stern
	Star    []RankRow // Rangliste mit Stern
	Pending []RankRow // in Bewertung (grau, n/7)
	NumJ    int

	LastScoreAt  *time.Time
	Finished     bool
	NowUnix      int64 // für die Uhr
	SlotLabel    string
	ScoringLabel string

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
	row   RankRow
	total int
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

	// Aktive Teilnehmer (ohne zurückgezogene).
	infos := make([]model.ParticipantInfo, 0, len(parts))
	for _, p := range parts {
		if p.Withdrawn {
			continue
		}
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
	ld.ScoringLabel = fmtDur(ld.Sched.ScoringSec)

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
			f := fullRank{row: row, total: total}
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
		return ld.Pending[i].P.SortOrder < ld.Pending[j].P.SortOrder
	})
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
		entries = append(entries, model.RankEntry{ID: f.row.P.ID, Scores: scores, Starred: f.row.P.Starred})
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
