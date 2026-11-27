// lastscored_test.go: grau hinterlegter zuletzt bewerteter Auftritt. Die
// Ranglisten- und die „In Bewertung"-Zeile des Teilnehmers mit der zeitlich
// letzten Note bekommen die Klasse last-scored (auch bei Korrekturen an
// einem früher bewerteten Auftritt).

package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"turnier/internal/model"
)

func TestLastScoredRowMarked(t *testing.T) {
	app, st := newTestApp(t, AuthConfig{})
	sid, cid, did, pA := seedFinished(t, app)
	pB, err := st.CreateParticipant(model.Participant{
		Discipline: did, StartNr: "2", Name: "Garde B", Starred: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := app.Handler()

	get := func() string {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/?s=%d&t=%d&d=%d", sid, cid, did), nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /: Status %d", w.Code)
		}
		return w.Body.String()
	}
	// Timestamps in der DB sind millisekundengenau – für eine zuverlässig
	// „spätere" Note kurz warten.
	tick := func() { time.Sleep(2 * time.Millisecond) }

	// Ohne Noten: keine Markierung.
	if strings.Contains(get(), "last-scored") {
		t.Fatal("ohne Noten ist eine Zeile markiert")
	}

	// B komplett bewerten (7/7): B landet in der Stern-Rangliste und ist
	// dort als zuletzt bewerteter Auftritt markiert.
	for j := 1; j <= 7; j++ {
		if err := st.SetScore(pB, j, 50+j); err != nil {
			t.Fatal(err)
		}
		tick()
	}
	if body := get(); !strings.Contains(body, `class="last-scored" title="Zuletzt bewertet"`) {
		t.Fatal("letzter Auftritt in der Rangliste nicht markiert")
	}

	// Note an A korrigieren (später, A bleibt unvollständig): Die Markierung
	// wandert zu A in die „In Bewertung"-Liste.
	if err := st.SetScore(pA, 2, 40); err != nil {
		t.Fatal(err)
	}
	body := get()
	if !strings.Contains(body, `class="pending last-scored"`) {
		t.Fatal("letzter Eintrag in der In-Bewertung-Liste nicht markiert")
	}
	if strings.Contains(body, `class="last-scored" title="Zuletzt bewertet"`) {
		t.Fatal("B ist weiterhin als zuletzt bewertet markiert")
	}
}
