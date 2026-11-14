// Package store ist die SQLite-Persistenzschicht (modernc.org/sqlite, ohne CGO).
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"tuniert/internal/model"
)

// Store kapselt den Datenbankzugriff.
type Store struct {
	db *sql.DB
}

// Open öffnet (bzw. legt an) die Datenbank unter path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single-Prozess-App: eine Verbindung reicht und meidet Write-Conflicts.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close schließt die Datenbank.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS session(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS competition(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			sort_order INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_comp_session ON competition(session_id)`,
		`CREATE TABLE IF NOT EXISTS discipline(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			competition_id INTEGER NOT NULL REFERENCES competition(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			max_dance_sec INTEGER NOT NULL DEFAULT 180,
			scoring_def_sec INTEGER NOT NULL DEFAULT 720,
			walk_on_sec INTEGER NOT NULL DEFAULT 120,
			num_judges INTEGER NOT NULL DEFAULT 7,
			sort_order INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_disc_comp ON discipline(competition_id)`,
		`CREATE TABLE IF NOT EXISTS participant(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			discipline_id INTEGER NOT NULL REFERENCES discipline(id) ON DELETE CASCADE,
			start_nr TEXT NOT NULL,
			name TEXT NOT NULL,
			verein TEXT NOT NULL DEFAULT '',
			starred INTEGER NOT NULL DEFAULT 0,
			withdrawn INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_part_disc ON participant(discipline_id, sort_order)`,
		`CREATE TABLE IF NOT EXISTS score(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			participant_id INTEGER NOT NULL REFERENCES participant(id) ON DELETE CASCADE,
			judge INTEGER NOT NULL,
			value INTEGER NOT NULL CHECK (value >= 0 AND value <= 100),
			updated_at TEXT NOT NULL,
			UNIQUE(participant_id, judge)
		)`,
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}
	// Bestehende DBs: Spalte für die offizielle Gesamtsumme nachrüsten.
	// "duplicate column name" ist der erwartete Zustand bei neuen DBs.
	if _, err := s.db.Exec(`ALTER TABLE participant ADD COLUMN total_override INTEGER`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migration: %w", err)
	}
	// Bestehende DBs: alte BDK-Test-Defaults (3:00 Tanz / 12:00 Punktezeit /
	// 2:00 Aufritt = 17-Minuten-Slot) durch den 5-Minuten-Takt ersetzen.
	// Nur Disziplinen mit den unveränderten Testwerten; bewusst angepasste
	// Zeiten bleiben erhalten.
	if _, err := s.db.Exec(`
		UPDATE discipline SET max_dance_sec = ?, scoring_def_sec = ?, walk_on_sec = ?
		WHERE max_dance_sec = 180 AND scoring_def_sec = 720 AND walk_on_sec = 120`,
		model.DefaultMaxDanceSec, model.DefaultScoringSec, model.DefaultWalkOnSec); err != nil {
		return fmt.Errorf("migration: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- Session ----------

func (s *Store) ListSessions() ([]model.Session, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM session ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Session{}
	for rows.Next() {
		var x model.Session
		var ca string
		if err := rows.Scan(&x.ID, &x.Name, &ca); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, ca); err == nil {
			x.CreatedAt = t
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSession(id int64) (*model.Session, error) {
	var x model.Session
	var ca string
	err := s.db.QueryRow(`SELECT id, name, created_at FROM session WHERE id = ?`, id).
		Scan(&x.ID, &x.Name, &ca)
	if err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, ca); err == nil {
		x.CreatedAt = t
	}
	return &x, nil
}

func (s *Store) CreateSession(name string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO session(name, created_at) VALUES(?, ?)`,
		name, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RenameSession(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE session SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) DeleteSession(id int64) error {
	_, err := s.db.Exec(`DELETE FROM session WHERE id = ?`, id)
	return err
}

// ---------- Competition ("Turnier") ----------

func (s *Store) ListCompetitions(sessionID int64) ([]model.Competition, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, name, sort_order FROM competition WHERE session_id = ? ORDER BY sort_order, id`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Competition{}
	for rows.Next() {
		var c model.Competition
		if err := rows.Scan(&c.ID, &c.SessionID, &c.Name, &c.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCompetition(id int64) (*model.Competition, error) {
	var c model.Competition
	err := s.db.QueryRow(`SELECT id, session_id, name, sort_order FROM competition WHERE id = ?`, id).
		Scan(&c.ID, &c.SessionID, &c.Name, &c.SortOrder)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CreateCompetition(sessionID int64, name string) (int64, error) {
	var maxOrder int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), 0) FROM competition WHERE session_id = ?`,
		sessionID).Scan(&maxOrder); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO competition(session_id, name, sort_order) VALUES(?, ?, ?)`,
		sessionID, name, maxOrder+1)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RenameCompetition(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE competition SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) DeleteCompetition(id int64) error {
	_, err := s.db.Exec(`DELETE FROM competition WHERE id = ?`, id)
	return err
}

// ---------- Disziplin ----------

func (s *Store) ListDisciplines(competitionID int64) ([]model.Discipline, error) {
	rows, err := s.db.Query(
		`SELECT id, competition_id, name, max_dance_sec, scoring_def_sec, walk_on_sec, num_judges, sort_order
		 FROM discipline WHERE competition_id = ? ORDER BY sort_order, id`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Discipline{}
	for rows.Next() {
		var d model.Discipline
		if err := rows.Scan(&d.ID, &d.CompetitionID, &d.Name, &d.MaxDanceSec,
			&d.ScoringDefSec, &d.WalkOnSec, &d.NumJudges, &d.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDiscipline(id int64) (*model.Discipline, error) {
	var d model.Discipline
	err := s.db.QueryRow(
		`SELECT id, competition_id, name, max_dance_sec, scoring_def_sec, walk_on_sec, num_judges, sort_order
		 FROM discipline WHERE id = ?`, id).
		Scan(&d.ID, &d.CompetitionID, &d.Name, &d.MaxDanceSec,
			&d.ScoringDefSec, &d.WalkOnSec, &d.NumJudges, &d.SortOrder)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDisciplineFull liefert Disziplin plus übergeordnete Namen (für die Kopfzeile).
func (s *Store) GetDisciplineFull(id int64) (*model.Discipline, error) {
	var d model.Discipline
	err := s.db.QueryRow(`
		SELECT di.id, di.competition_id, di.name, di.max_dance_sec, di.scoring_def_sec,
		       di.walk_on_sec, di.num_judges, di.sort_order, c.name, se.name
		FROM discipline di
		JOIN competition c ON c.id = di.competition_id
		JOIN session se ON se.id = c.session_id
		WHERE di.id = ?`, id).
		Scan(&d.ID, &d.CompetitionID, &d.Name, &d.MaxDanceSec,
			&d.ScoringDefSec, &d.WalkOnSec, &d.NumJudges, &d.SortOrder,
			&d.CompetitionName, &d.SessionName)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// FirstDisciplineID liefert die erste Disziplin (für Default-Views).
func (s *Store) FirstDisciplineID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		SELECT di.id FROM discipline di
		JOIN competition c ON c.id = di.competition_id
		ORDER BY c.session_id, c.sort_order, c.id, di.sort_order, di.id
		LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) CreateDiscipline(competitionID int64, d model.Discipline) (int64, error) {
	if d.MaxDanceSec <= 0 {
		d.MaxDanceSec = model.DefaultMaxDanceSec
	}
	if d.ScoringDefSec <= 0 {
		d.ScoringDefSec = model.DefaultScoringSec
	}
	if d.WalkOnSec <= 0 {
		d.WalkOnSec = model.DefaultWalkOnSec
	}
	if d.NumJudges <= 0 {
		d.NumJudges = model.DefaultNumJudges
	}
	var maxOrder int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), 0) FROM discipline WHERE competition_id = ?`,
		competitionID).Scan(&maxOrder); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO discipline(competition_id, name, max_dance_sec, scoring_def_sec, walk_on_sec, num_judges, sort_order)
		 VALUES(?,?,?,?,?,?,?)`,
		competitionID, d.Name, d.MaxDanceSec, d.ScoringDefSec, d.WalkOnSec, d.NumJudges, maxOrder+1)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateDiscipline(d model.Discipline) error {
	_, err := s.db.Exec(
		`UPDATE discipline SET name=?, max_dance_sec=?, scoring_def_sec=?, walk_on_sec=?, num_judges=? WHERE id=?`,
		d.Name, d.MaxDanceSec, d.ScoringDefSec, d.WalkOnSec, d.NumJudges, d.ID)
	return err
}

func (s *Store) DeleteDiscipline(id int64) error {
	_, err := s.db.Exec(`DELETE FROM discipline WHERE id = ?`, id)
	return err
}

// ---------- Teilnehmer ----------

func (s *Store) ListParticipants(disciplineID int64) ([]model.Participant, error) {
	rows, err := s.db.Query(
		`SELECT id, discipline_id, start_nr, name, verein, starred, withdrawn, sort_order, total_override
		 FROM participant WHERE discipline_id = ? ORDER BY sort_order, id`, disciplineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Participant{}
	for rows.Next() {
		var p model.Participant
		var st, w int
		var to sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Discipline, &p.StartNr, &p.Name, &p.Verein,
			&st, &w, &p.SortOrder, &to); err != nil {
			return nil, err
		}
		p.Starred = st == 1
		p.Withdrawn = w == 1
		if to.Valid {
			v := int(to.Int64)
			p.TotalOverride = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListParticipantsWithScores liefert die Teilnehmer mit allen Noten (für Live-Ansicht/Zeitplan).
func (s *Store) ListParticipantsWithScores(disciplineID int64) ([]model.Participant, error) {
	parts, err := s.ListParticipants(disciplineID)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return parts, nil
	}
	ids := make([]any, len(parts))
	ph := make([]string, len(parts))
	for i, p := range parts {
		ids[i] = p.ID
		ph[i] = "?"
	}
	rows, err := s.db.Query(
		`SELECT participant_id, judge, value, updated_at FROM score
		 WHERE participant_id IN (`+strings.Join(ph, ",")+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int64][]model.Score, len(parts))
	for _, p := range parts {
		byID[p.ID] = []model.Score{}
	}
	for rows.Next() {
		var pid, judge, value int64
		var ua string
		if err := rows.Scan(&pid, &judge, &value, &ua); err != nil {
			return nil, err
		}
		byID[pid] = append(byID[pid], model.Score{
			Judge: int(judge), Value: int(value),
			UpdatedAt: parseTime(ua),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Noten nach Richter sortieren.
	for i, p := range parts {
		sc := byID[p.ID]
		for a := 0; a < len(sc); a++ {
			for b := a + 1; b < len(sc); b++ {
				if sc[b].Judge < sc[a].Judge {
					sc[a], sc[b] = sc[b], sc[a]
				}
			}
		}
		parts[i].Scores = sc
	}
	return parts, nil
}

func (s *Store) GetParticipant(id int64) (*model.Participant, error) {
	var p model.Participant
	var st, w int
	var to sql.NullInt64
	err := s.db.QueryRow(
		`SELECT id, discipline_id, start_nr, name, verein, starred, withdrawn, sort_order, total_override
		 FROM participant WHERE id = ?`, id).
		Scan(&p.ID, &p.Discipline, &p.StartNr, &p.Name, &p.Verein, &st, &w, &p.SortOrder, &to)
	if err != nil {
		return nil, err
	}
	p.Starred = st == 1
	p.Withdrawn = w == 1
	if to.Valid {
		v := int(to.Int64)
		p.TotalOverride = &v
	}
	return &p, nil
}

// SetTotalOverride setzt die offizielle Gesamtsumme (total = nil löscht
// die Überschreibung, es wird wieder aus den Noten berechnet).
func (s *Store) SetTotalOverride(participantID int64, total *int) error {
	var v any
	if total != nil {
		v = *total
	}
	_, err := s.db.Exec(
		`UPDATE participant SET total_override = ? WHERE id = ?`, v, participantID)
	return err
}

func (s *Store) CreateParticipant(p model.Participant) (int64, error) {
	var maxOrder int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), 0) FROM participant WHERE discipline_id = ?`,
		p.Discipline).Scan(&maxOrder); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO participant(discipline_id, start_nr, name, verein, starred, withdrawn, sort_order)
		 VALUES(?,?,?,?,?,?,?)`,
		p.Discipline, p.StartNr, p.Name, p.Verein, boolInt(p.Starred), boolInt(p.Withdrawn), maxOrder+1)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateParticipant(p model.Participant) error {
	_, err := s.db.Exec(
		`UPDATE participant SET start_nr=?, name=?, verein=?, starred=?, withdrawn=?, sort_order=? WHERE id=?`,
		p.StartNr, p.Name, p.Verein, boolInt(p.Starred), boolInt(p.Withdrawn), p.SortOrder, p.ID)
	return err
}

// UpsertParticipantByStartNr legt einen Teilnehmer an oder aktualisiert ihn
// (beim CSV-Import, idempotent pro Disziplin).
func (s *Store) UpsertParticipantByStartNr(disciplineID int64, p model.Participant) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM participant WHERE discipline_id = ? AND start_nr = ?`,
		disciplineID, p.StartNr).Scan(&id)
	if err == nil {
		if err := s.UpdateParticipant(model.Participant{
			ID: id, Discipline: disciplineID, StartNr: p.StartNr, Name: p.Name,
			Verein: p.Verein, Starred: p.Starred, Withdrawn: p.Withdrawn,
		}); err != nil {
			return 0, err
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	return s.CreateParticipant(model.Participant{
		Discipline: disciplineID, StartNr: p.StartNr, Name: p.Name,
		Verein: p.Verein, Starred: p.Starred, Withdrawn: p.Withdrawn,
	})
}

func (s *Store) DeleteParticipant(id int64) error {
	_, err := s.db.Exec(`DELETE FROM participant WHERE id = ?`, id)
	return err
}

// MoveParticipant verschiebt einen Teilnehmer in der Startreihenfolge
// (dir = -1 hoch, +1 runter).
func (s *Store) MoveParticipant(id int64, dir int) error {
	p, err := s.GetParticipant(id)
	if err != nil {
		return err
	}
	op, order := "<", "ASC"
	if dir > 0 {
		op, order = ">", "DESC"
	}
	q := `SELECT id, sort_order FROM participant WHERE discipline_id = ?
	      AND sort_order ` + op + ` ? ORDER BY sort_order ` + order + ` LIMIT 1`
	var otherID, otherOrder int64
	err = s.db.QueryRow(q, p.Discipline, p.SortOrder).Scan(&otherID, &otherOrder)
	if err == sql.ErrNoRows {
		return nil // am Ende der Liste
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		UPDATE participant SET sort_order =
			CASE id WHEN ? THEN ? WHEN ? THEN ? END
		WHERE id IN (?, ?)`, id, otherOrder, otherID, p.SortOrder, id, otherID)
	return err
}

// ---------- Noten ----------

// SetScore setzt (bzw. überschreibt) die Note von judge (1..n) für einen Teilnehmer.
func (s *Store) SetScore(participantID int64, judge, value int) error {
	_, err := s.db.Exec(`
		INSERT INTO score(participant_id, judge, value, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(participant_id, judge)
		DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		participantID, judge, value, time.Now().Format(time.RFC3339Nano))
	return err
}

// ClearScore entfernt die Note von judge.
func (s *Store) ClearScore(participantID int64, judge int) error {
	_, err := s.db.Exec(
		`DELETE FROM score WHERE participant_id = ? AND judge = ?`, participantID, judge)
	return err
}

// ResetScores löscht alle Noten einer Disziplin (inkl. offizieller
// Summen-Überschreibungen – die alten Werte wären sonst veraltet).
func (s *Store) ResetScores(disciplineID int64) error {
	if _, err := s.db.Exec(`
		DELETE FROM score WHERE participant_id IN
			(SELECT id FROM participant WHERE discipline_id = ?)`, disciplineID); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`UPDATE participant SET total_override = NULL WHERE discipline_id = ?`, disciplineID)
	return err
}

// ResetAllScores löscht alle Noten der Datenbank (Admin-Danger-Zone).
func (s *Store) ResetAllScores() error {
	if _, err := s.db.Exec(`DELETE FROM score`); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE participant SET total_override = NULL`)
	return err
}

// LastScoreTime liefert den Zeitpunkt der letzten Note einer Disziplin.
func (s *Store) LastScoreTime(disciplineID int64) (time.Time, bool, error) {
	var ua string
	err := s.db.QueryRow(`
		SELECT MAX(updated_at) FROM score
		WHERE participant_id IN (SELECT id FROM participant WHERE discipline_id = ?)`,
		disciplineID).Scan(&ua)
	if err != nil || ua == "" {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339Nano, ua)
	return t, true, err
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
