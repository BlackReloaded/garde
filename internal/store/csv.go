package store

import (
	"bufio"
	"database/sql"
	"fmt"
	"strings"

	"turnier/internal/model"
)

// CSVHeader ist die Kopfzeile der Import-/Exportdatei.
const CSVHeader = "session;turnier;disziplin;startnr;name;verein;star;withdrawn"

type csvRow struct {
	Session, Comp, Disc string
	P                   model.Participant
}

// parseCSV liest eine CSV-Datei (Trenner ; oder ,; Kopfzeile optional).
// Zeilenreihenfolge = Startreihenfolge.
func parseCSV(text string) ([]csvRow, error) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var out []csvRow
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sep := ";"
		if !strings.Contains(line, ";") {
			sep = ","
		}
		f := strings.Split(line, sep)
		if lineNo == 1 && strings.EqualFold(strings.TrimSpace(f[0]), "session") {
			continue // Kopfzeile
		}
		if len(f) < 6 {
			return nil, fmt.Errorf("Zeile %d: erwartet 6–8 Spalten, gefunden %d", lineNo, len(f))
		}
		p := model.Participant{
			StartNr:   strings.TrimSpace(f[3]),
			Name:      strings.TrimSpace(f[4]),
			Verein:    strings.TrimSpace(f[5]),
			Starred:   flagValue(col(f, 6)),
			Withdrawn: flagValue(col(f, 7)),
		}
		if p.StartNr == "" || p.Name == "" {
			return nil, fmt.Errorf("Zeile %d: startnr und name sind Pflicht", lineNo)
		}
		out = append(out, csvRow{
			Session: strings.TrimSpace(f[0]),
			Comp:    strings.TrimSpace(f[1]),
			Disc:    strings.TrimSpace(f[2]),
			P:       p,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("keine Zeilen in der CSV-Datei")
	}
	return out, nil
}

func col(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// flagValue wertet eine Flag-Spalte aus ("1", "x", "*", "ja" -> true).
func flagValue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "x", "*", "ja", "true", "yes":
		return true
	}
	return false
}

// ImportCSV importiert eine CSV-Datei. Session/Turnier/Disziplin werden
// nach Name angelegt oder wiederverwendet; Teilnehmer werden idempotent
// pro (Disziplin, Startnr) angelegt bzw. aktualisiert.
func (s *Store) ImportCSV(text string) (int, error) {
	rows, err := parseCSV(text)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		compID, err := s.getOrCreateCompetition(r.Session, r.Comp)
		if err != nil {
			return 0, fmt.Errorf("%s/%s: %w", r.Session, r.Comp, err)
		}
		discID, err := s.getOrCreateDiscipline(compID, r.Disc)
		if err != nil {
			return 0, fmt.Errorf("%s/%s/%s: %w", r.Session, r.Comp, r.Disc, err)
		}
		if _, err := s.UpsertParticipantByStartNr(discID, r.P); err != nil {
			return 0, fmt.Errorf("%s %s: %w", r.P.StartNr, r.P.Name, err)
		}
	}
	return len(rows), nil
}

func (s *Store) getOrCreateSession(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM session WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	return s.CreateSession(name)
}

func (s *Store) getOrCreateCompetition(sessionName, compName string) (int64, error) {
	sid, err := s.getOrCreateSession(sessionName)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(
		`SELECT id FROM competition WHERE session_id = ? AND name = ? COLLATE NOCASE`,
		sid, compName).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	return s.CreateCompetition(sid, compName)
}

func (s *Store) getOrCreateDiscipline(compID int64, discName string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM discipline WHERE competition_id = ? AND name = ? COLLATE NOCASE`,
		compID, discName).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	return s.CreateDiscipline(compID, model.Discipline{Name: discName})
}

// ExportCSV rendert alle Daten einer Session als CSV-Text.
func (s *Store) ExportCSV(sessionID int64) (string, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	comps, err := s.ListCompetitions(sessionID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(CSVHeader + "\n")
	for _, c := range comps {
		discs, err := s.ListDisciplines(c.ID)
		if err != nil {
			return "", err
		}
		for _, d := range discs {
			parts, err := s.ListParticipants(d.ID)
			if err != nil {
				return "", err
			}
			for _, p := range parts {
				fmt.Fprintf(&b, "%s;%s;%s;%s;%s;%s;%d;%d\n",
					sess.Name, c.Name, d.Name,
					p.StartNr, p.Name, p.Verein, boolInt(p.Starred), boolInt(p.Withdrawn))
			}
		}
	}
	return b.String(), nil
}
