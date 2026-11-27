// offline_test.go: Die Browser-Outbox schickt offline erfasste Noten später
// nach und liefert dabei ihren Erfassungszeitpunkt im Feld "at" mit.
// Plausible Zeitstempel übernehmen wir als updated_at (Zeitplan-ETA und die
// "zuletzt bewertet"-Markierung folgen so der echten Reihenfolge);
// unplausible (Zukunft, älter als 24 h) werden auf die aktuelle Zeit gezogen.

package web

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"turnier/internal/model"
)

func TestScoreOfflineTimestamp(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	// Eigener Scorer-Nutzer (Rolle in der login_role-Tabelle nötig, damit
	// der POST den Auth-Guard passiert).
	const mail = "offline-judge@example.test"
	uid, err := st.UpsertLoginUser(model.LoginUser{
		Provider: "p", ProviderSub: "off", Email: mail, Role: model.RoleScorer})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginSession("tok-off", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.AddLoginRole(model.RoleScorer, mail); err != nil {
		t.Fatal(err)
	}
	ck := &http.Cookie{Name: sessionCookie, Value: "tok-off"}
	_, _, did, pid := seedFinished(t, app)
	mux := app.Handler()

	// Zeitstempel der gespeicherten Noten, indiziert nach Richter-Nummer.
	scoreTimes := func() map[int]time.Time {
		parts, err := st.ListParticipantsWithScores(did)
		if err != nil || len(parts) != 1 {
			t.Fatalf("Teilnehmer/Noten lesen: %v", err)
		}
		out := map[int]time.Time{}
		for _, s := range parts[0].Scores {
			out[s.Judge] = s.UpdatedAt
		}
		return out
	}

	// 1. Plausibler at (2 h in der Vergangenheit): wird übernommen.
	at := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	body := fmt.Sprintf("participant_id=%d&judge=1&value=74&at=%s", pid, at.Format(time.RFC3339Nano))
	if w := postForm(mux, "/score", body, ck); w.Code != http.StatusOK {
		t.Fatalf("Status %d, wollten 200", w.Code)
	}
	if got := scoreTimes()[1]; !got.Equal(at) {
		t.Fatalf("Erfassungszeit nicht übernommen: %v statt %v", got, at)
	}

	// 2. Unplausibler at (Jahr 2020): ignoriert, aktuelle Zeit zählt.
	body = fmt.Sprintf("participant_id=%d&judge=2&value=80&at=2020-01-01T00:00:00Z", pid)
	if w := postForm(mux, "/score", body, ck); w.Code != http.StatusOK {
		t.Fatalf("Status %d, wollten 200", w.Code)
	}
	if got := scoreTimes()[2]; time.Since(got) > time.Minute {
		t.Fatalf("unplausibler at übernommen: %v", got)
	}

	// 3. Zukunfts-Zeitstempel (+2 h): ebenfalls auf jetzt gezogen.
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	body = fmt.Sprintf("participant_id=%d&judge=3&value=65&at=%s", pid, future)
	if w := postForm(mux, "/score", body, ck); w.Code != http.StatusOK {
		t.Fatalf("Status %d, wollten 200", w.Code)
	}
	if got := scoreTimes()[3]; time.Since(got) > time.Minute {
		t.Fatalf("Zukunfts-Zeitstempel übernommen: %v", got)
	}

	// 4. Ohne at (normaler Online-Weg): Zeitstempel ist jetzt.
	body = fmt.Sprintf("participant_id=%d&judge=4&value=70", pid)
	if w := postForm(mux, "/score", body, ck); w.Code != http.StatusOK {
		t.Fatalf("Status %d, wollten 200", w.Code)
	}
	if got := scoreTimes()[4]; time.Since(got) > time.Minute {
		t.Fatalf("ohne at kein aktueller Zeitstempel: %v", got)
	}
}
