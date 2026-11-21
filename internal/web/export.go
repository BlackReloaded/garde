package web

// PDF-Ergebnisexport: die Wertung der jeweils gewählten Disziplin als
// Wertungsheft bzw. Protokoll – wie der Bogen, den ein Kampfrichter führt:
// alle komplett bewerteten Tänze in Startreihenfolge mit den Einzelnoten
// der Richter, Gesamtsumme und Platz. Bewusst reines Go über go-pdf/fpdf
// mit Standard-Schrift Helvetica (cp1252): kein externer Dienst, kein
// Headless-Browser, das Binary bleibt autonom und offlinefähig.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"turnier/internal/schedule"
)

// Blattmaß Querformat A4 in mm (Platz für 7+ Notenspalten).
const (
	pdfPageW  = 297.0
	pdfPageH  = 210.0
	pdfMargin = 10.0
	pdfRowH   = 5.4
)

// exportProtocol behandelt GET /export/protocol?s=&t=&d= (Selektoren wie
// auf der Seite). Reines Lese-Recht wie die Live-Ansicht selbst – der Knopf
// funktioniert deshalb auch ohne Anmeldung.
func (a *App) exportProtocol(w http.ResponseWriter, r *http.Request) {
	_, _, discID := a.resolveView(r)
	if discID == 0 {
		http.NotFound(w, r)
		return
	}
	ld, err := a.loadLive(discID, time.Now())
	if err != nil {
		http.Error(w, "Export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf := new(bytes.Buffer)
	if err := protocolPDF(ld, buf); err != nil {
		http.Error(w, "Export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+pdfFileName(ld.Disc.Name)+`"`)
	w.Write(buf.Bytes())
}

// protocolRow ist eine Zeile des Wertungsbogens.
type protocolRow struct {
	startNr string
	name    string
	verein  string
	scores  []string // Noten nach Richter; "-" bei fehlend, Klammern = gestrichen
	total   string   // Gesamtsumme, "*" bei offiziell genannter Summe
	rank    int      // 0 = keine Platzierung
}

// protocolRows baut den Wertungsbogen: komplett bewertete Tänze in
// Startreihenfolge (nicht in Platzordnung – das ist die Heft-Logik).
// Gestrichene Noten erscheinen in Klammern, die offiziell genannte Summe
// mit "*", Sternchen-Tänze mit "(S)" vor dem Namen.
func protocolRows(ld *LiveData) []protocolRow {
	byID := make(map[int64]RankRow, len(ld.Plain)+len(ld.Star))
	starred := make(map[int64]bool, len(ld.Star))
	for _, row := range ld.Plain {
		byID[row.P.ID] = row
	}
	for _, row := range ld.Star {
		byID[row.P.ID] = row
		starred[row.P.ID] = true
	}
	rows := make([]protocolRow, 0, len(byID))
	for _, p := range ld.Active {
		row, ok := byID[p.ID]
		if !ok {
			continue // noch nicht komplett bewertet
		}
		name := p.Name
		if starred[p.ID] {
			name = "(S) " + name
		}
		scores := make([]string, 0, ld.NumJ)
		for i, v := range row.Values {
			switch {
			case v < 0:
				scores = append(scores, "-")
			case i == row.LowIdx || i == row.HighIdx:
				scores = append(scores, "("+strconv.Itoa(v)+")")
			default:
				scores = append(scores, strconv.Itoa(v))
			}
		}
		total := strconv.Itoa(row.Total)
		if row.Official {
			total += " *"
		}
		rows = append(rows, protocolRow{
			startNr: p.StartNr, name: name, verein: p.Verein,
			scores: scores, total: total, rank: row.Rank,
		})
	}
	return rows
}

// protocolPDF schreibt das Ergebnisprotokoll von ld als PDF nach w.
func protocolPDF(ld *LiveData, w io.Writer) error {
	usableW := pdfPageW - 2*pdfMargin
	pdf := fpdf.New("L", "mm", "A4", "")
	if err := pdf.Error(); err != nil {
		return err
	}
	pdf.SetCompression(false)
	pdf.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	// Eigene Umbrüche: damit auf jeder Seite der Spaltenkopf steht.
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetTitle(to1252("Ergebnisprotokoll "+ld.Disc.Name), false)

	rows := protocolRows(ld)
	pdf.AddPage()

	// Kopf: Titel + Herkunftszeile + Lesehilfe.
	pdf.SetFont("Helvetica", "B", 14)
	pdf.SetXY(pdfMargin, pdfMargin+1)
	pdf.Cell(usableW, 8, fit1252(pdf, "Ergebnisprotokoll – "+ld.Disc.Name, usableW))

	done := len(ld.Plain) + len(ld.Star)
	total := ld.ActivePlain + ld.ActiveStar
	meta := fmt.Sprintf("%s · %s · %d von %d Tänzern fertig bewertet · Stand: %s",
		ld.Disc.SessionName, ld.Disc.CompetitionName, done, total,
		time.Unix(ld.NowUnix, 0).In(schedule.Zone).Format("02.01.2006, 15:04"))
	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetXY(pdfMargin, pdfMargin+11.5)
	pdf.Cell(usableW, 5, to1252(meta))

	legend := "Klammern = Note zählt nicht zur Summe (Streichregel) · * = offiziell genannte Summe · (S) = Sternchen"
	pdf.SetFont("Helvetica", "I", 7)
	pdf.SetXY(pdfMargin, pdfMargin+17.5)
	pdf.Cell(usableW, 4, to1252(legend))
	pdf.SetDrawColor(120, 120, 120)
	pdf.Line(pdfMargin, pdfMargin+23, pdfPageW-pdfMargin, pdfMargin+23)

	if len(rows) == 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetXY(pdfMargin, pdfMargin+30)
		pdf.Cell(usableW, 6, to1252("Noch keine komplette Wertung – das Protokoll füllt sich, sobald alle Noten erfasst sind."))
		return finishPDF(pdf, w)
	}

	nrW, nameW, vereinW, scoreW, sumW, platzW := pdfColumnWidths(ld.NumJ)

	// Spaltenkopf; wird nach jedem Seitenumbruch wiederholt.
	drawHeader := func(y float64) {
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetXY(pdfMargin, y)
		pdf.CellFormat(nrW, pdfRowH, "Start", "", 0, "C", false, 0, "")
		pdf.CellFormat(nameW, pdfRowH, "Name", "", 0, "L", false, 0, "")
		pdf.CellFormat(vereinW, pdfRowH, "Verein", "", 0, "L", false, 0, "")
		for j := 1; j <= ld.NumJ; j++ {
			pdf.CellFormat(scoreW, pdfRowH, "R"+strconv.Itoa(j), "", 0, "C", false, 0, "")
		}
		pdf.CellFormat(sumW, pdfRowH, "Gesamt", "", 0, "C", false, 0, "")
		pdf.CellFormat(platzW, pdfRowH, "Platz", "", 0, "C", false, 0, "")
		pdf.SetDrawColor(120, 120, 120)
		y2 := y + pdfRowH + 0.8
		pdf.Line(pdfMargin, y2, pdfPageW-pdfMargin, y2)
	}

	y := pdfMargin + 30 + pdfRowH + 2.5
	drawHeader(pdfMargin + 30)
	for _, row := range rows {
		if y+pdfRowH > pdfPageH-pdfMargin-4 {
			pdf.AddPage()
			y = pdfMargin + 2
			drawHeader(y)
			y += pdfRowH + 2.5
		}
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetXY(pdfMargin, y)
		pdf.CellFormat(nrW, pdfRowH, to1252(row.startNr), "", 0, "C", false, 0, "")
		pdf.CellFormat(nameW, pdfRowH, fit1252(pdf, row.name, nameW-1.5), "", 0, "L", false, 0, "")
		pdf.CellFormat(vereinW, pdfRowH, fit1252(pdf, row.verein, vereinW-1.5), "", 0, "L", false, 0, "")
		for _, sc := range row.scores {
			pdf.CellFormat(scoreW, pdfRowH, to1252(sc), "", 0, "C", false, 0, "")
		}
		pdf.CellFormat(sumW, pdfRowH, to1252(row.total), "", 0, "C", false, 0, "")
		rank := "-"
		if row.rank > 0 {
			rank = strconv.Itoa(row.rank)
		}
		pdf.CellFormat(platzW, pdfRowH, rank, "", 0, "C", false, 0, "")
		y += pdfRowH
		pdf.SetDrawColor(225, 225, 225)
		pdf.Line(pdfMargin, y, pdfPageW-pdfMargin, y)
		y += 0.9
	}
	return finishPDF(pdf, w)
}

func finishPDF(pdf *fpdf.Fpdf, w io.Writer) error {
	if err := pdf.Error(); err != nil {
		return err
	}
	return pdf.Output(w)
}

// pdfColumnWidths verteilt die Tabellenbreite je nach Richterzahl: wo die
// Notenspalten zu eng würden, geben Name und Verein Raum ab.
func pdfColumnWidths(numJ int) (nrW, nameW, vereinW, scoreW, sumW, platzW float64) {
	usable := pdfPageW - 2*pdfMargin
	nrW, sumW, platzW = 14.0, 24.0, 13.0
	nameW, vereinW = 56.0, 42.0
	if numJ < 1 {
		numJ = 1
	}
	scoreW = (usable - nrW - nameW - vereinW - sumW - platzW) / float64(numJ)
	if scoreW > 15 {
		scoreW = 15
	}
	if scoreW < 9 {
		nameW, vereinW = 40, 30
		scoreW = (usable - nrW - nameW - vereinW - sumW - platzW) / float64(numJ)
		if scoreW < 6 {
			scoreW = 6
		}
	}
	return
}

// fit1252 kürzt Text (in cp1252-Kodierung) mit "…" (= 0x85), bis er in
// w mm passt. Gemessen wird IMMER die konvertierte Form – die
// Standard-Schriften von fpdf zählen bytes, keine Unicodezeichen.
func fit1252(pdf *fpdf.Fpdf, s string, w float64) string {
	if pdf.GetStringWidth(to1252(s)) <= w {
		return to1252(s)
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		cand := to1252(strings.TrimRight(string(r), " ") + "\u2026")
		if pdf.GetStringWidth(cand) <= w {
			return cand
		}
	}
	return to1252("\u2026")
}

// to1252 wandelt UTF-8 in die bytes-Kodierung der Standard-Schriften um
// (Windows/Latin-1). fpdf interpretiert bei diesen Schriften jedes Byte
// einzeln nach cp1252 – UTF-8 direkt hindurchzugeben ergibt Mojibake.
// Unbekannte Zeichen werden zu "?", damit nie Müll im PDF steht.
func to1252(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteByte(byte(r))
		case r <= 0xFF:
			b.WriteByte(byte(r)) // Latin-1 = cp1252
		default:
			if m, ok := cp1252Extra[r]; ok {
				b.WriteByte(m)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

// cp1252Extra: Zeichen über U+00FF, die in der Anzeige vorkommen können
// (Gedankenstrich, Anführungszeichen, Stern → ASCII-Ersatz).
var cp1252Extra = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84, 0x2026: 0x85,
	0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88, 0x2030: 0x89, 0x0160: 0x8A,
	0x2039: 0x8B, 0x0152: 0x8C, 0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92,
	0x201C: 0x93, 0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97,
	0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B, 0x0153: 0x9C,
	0x017E: 0x9E, 0x0178: 0x9F,
	'★': '*',
}

// pdfFileName macht aus dem Disziplinen-Namen einen sicheren Dateinamen:
// Umlaut-Buchstaben werden umgeschrieben, alles außer Buchstaben, Ziffern
// und Bindestrich fällt weg (leere Reste zu "export").
func pdfFileName(discName string) string {
	s := strings.NewReplacer(
		"Ä", "Ae", "Ö", "Oe", "Ü", "Ue",
		"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	).Replace(discName)
	var b strings.Builder
	b.WriteString("ergebnisprotokoll-")
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r += 32
		}
		if r == ' ' || r == '/' || r == '\\' {
			r = '-'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "ergebnisprotokoll" {
		out = "ergebnisprotokoll-export"
	}
	return out + ".pdf"
}
