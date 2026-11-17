package model

import "time"

// Rollen für die Login-Berechtigungen.
const (
	// RoleAdmin darf alles: Verwaltung (Admin-Bereich) und Noten-Eingabe.
	RoleAdmin = "admin"
	// RoleScorer darf nur Noten eintragen (POST /score).
	RoleScorer = "bewerten"
)

// LoginUser ist ein per OIDC angemeldeter Benutzer. Provider ist die
// Issuer-URL des OIDC-Anbieters, ProviderSub dessen (stabile) sub-Claim.
type LoginUser struct {
	ID          int64
	Provider    string
	ProviderSub string
	Email       string
	Name        string
	Role        string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// LoginSession ist eine Server-Session (Token liegt im HttpOnly-Cookie).
type LoginSession struct {
	Token     string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// RoleEntry ist ein Eintrag der Rollen-Liste (DB-Tabelle login_role,
// die einzige Quelle – gefüllt über die Admin-UI, beim Start optional
// per -admins).
type RoleEntry struct {
	Role  string
	Email string
}
