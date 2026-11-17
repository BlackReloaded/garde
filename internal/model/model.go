// Package model enthält die Domänen-Typen und die Wertungslogik
// (7 Richter, beste + schlechteste Note gestrichen, Summe der mittleren 5,
// Ranking mit Tiebreak über alle Einzelnoten).
package model

import "time"

// Default-Einstellungen für eine Disziplin: 5-Minuten-Takt pro Tanz
// (1:00 Aufritt + 2:00 Tanz + 2:00 Punktevergabe).
const (
	DefaultMaxDanceSec = 120 // 2:00 max. Tanzlänge
	DefaultScoringSec  = 120 // 2:00 Punktevergabe
	DefaultWalkOnSec   = 60  // 1:00 Aufritt
	DefaultNumJudges   = 7
	MaxScore           = 100
)

// Session ist die oberste Ebene: ein Jahr (eine Saison), z. B. "2026/27".
type Session struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// Competition ("Turnier") gruppiert Disziplinen innerhalb einer Session
// (z. B. "Jugend", "Junioren", "Ü15").
type Competition struct {
	ID        int64
	SessionID int64
	Name      string
	SortOrder int
	// StartAtSec ist die geplante Startzeit des Turniers in Sekunden seit
	// Mitternacht (0 = nicht gesetzt), z. B. 9*3600+30*60 = 09:30.
	StartAtSec int
	// BreakSec ist die Pause zwischen zwei Disziplinen (0 = keine Pause).
	BreakSec int
}

// Disziplin ist eine bewertete Kategorie (z. B. "Solo weiblich", "Garde").
type Discipline struct {
	ID              int64
	CompetitionID   int64
	Name            string
	MaxDanceSec     int
	ScoringDefSec   int
	WalkOnSec       int
	NumJudges       int
	SortOrder       int
	CompetitionName string
	SessionName     string
}

// Participant ist ein Tanzer/eine Tanzerin oder Gruppe.
type Participant struct {
	ID         int64
	Discipline int64
	StartNr    string
	Name       string
	Verein     string
	Starred    bool
	Withdrawn  bool
	SortOrder  int
	Scores     []Score
	// TotalOverride ist die offiziell genannte Gesamtsumme. NULL = Summe
	// wird aus den Einzelnoten berechnet. Tritt auf, wenn die offiziell
	// verkündete Summe nicht zur berechneten passt.
	TotalOverride *int
}

// Score ist eine Richter-Note (0–100, ganzzahlig).
type Score struct {
	Judge     int
	Value     int
	UpdatedAt time.Time
}

// Status eines Teilnehmers aus Sicht des Zeitplans.
type State string

const (
	StateWaiting  State = "waiting"  // noch keine Noten
	StateScoring  State = "scoring"  // 1..n-1 Noten
	StateComplete State = "complete" // alle Noten
)

// ScoreAt ist eine Note mit Zeitstempel (für die Zeitprognose).
type ScoreAt struct {
	Value int
	At    time.Time
}

// ParticipantInfo ist die für den Zeitplan bereinigte Sicht auf einen Teilnehmer.
type ParticipantInfo struct {
	ID      int64
	StartNr string
	Name    string
	Verein  string
	Starred bool
	Scores  []ScoreAt
}
