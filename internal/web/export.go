package web

// PDF-Ergebnisexport: die Wertung als Wertungsheft bzw. Protokoll – wie der
// Bogen, den ein Kampfrichter führt: alle komplett bewerteten Tänze in
// Startreihenfolge mit den Einzelnoten der Richter, Gesamtsumme und Platz.
// Zwei Varianten: eine Disziplin (GET /export/protocol) und ein ganzes
// Turnier mit einem Blatt je Disziplin (GET /export/competition). Beide
// Routen haben dieselben Daten zusätzlich als CSV im PDF eingebettet
// (Dokument-Anhang) – dieselbe CSV gibt es auch direkt (*.csv).
//
// Bewusst reines Go über go-pdf/fpdf mit Standard-Schrift Helvetica
// (cp1252): kein externer Dienst, kein Headless-Browser, das Binary bleibt
// autonom und offlinefähig.

import (
	"bytes"
	"errors"
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
	// pdfBodyBottom = unterste mögliche Zeile; darunter sitzt die Fußzeile.
	pdfBodyBottom = pdfPageH - pdfMargin - 10
)

// errNoView: keine (gültige) Disziplin bzw. kein Turnier ausgewählt → 404.
var errNoView = errors.New("Ansicht nicht gefunden")

// protocolRow ist eine Zeile des Wertungsbogens. Die Werte stehen hier noch
// roh – PDF und CSV teilen sich dieselbe Quelle.
type protocolRow struct {
	startNr  string
	name     string
	verein   string
	star     bool
	scores   []int // Noten nach Richter; -1 = fehlend
	lowIdx   int   // Index der gestrichenen Mindestnote (-1 = keine)
	highIdx  int
	total    int
	official bool // Summe offiziell genannt
	rank     int  // 0 = keine Platzierung
}

// protocolSheet ist ein Blatt des Protokolls: eine Disziplin.
type protocolSheet struct {
	disc  string // Disziplinname (CSV-Spalte)
	label string // "Turnier · Disziplin" für Kopf- und Fußzeile
	meta  string // Herkunfts-/Standzeile unter dem Titel
	note  string // Zusatzzeile (nur erstes Blatt), z. B. Skipped-Liste
	numJ  int    // Notenspalten
	rows  []protocolRow
}

// protocolDoc ist ein komplettes Protokoll-PDF.
type protocolDoc struct {
	title    string // PDF-Titel (Metadaten)
	filename string // Dateiname fürs PDF
	csvName  string // Dateiname des CSV-Anhangs
	csv      []byte
	sheets   []protocolSheet
}

// ---------- Routen ----------

// exportProtocol beantwortet die beiden PDF-Routen:
//
//	GET /export/protocol     – eine Disziplin (Selektoren wie auf der Seite)
//	GET /export/competition  – ein Turnier, ein Blatt je Disziplin
//
// Reines Lese-Recht wie die Live-Ansicht selbst – die Knöpfe funktionieren
// deshalb auch ohne Anmeldung.
func (a *App) exportProtocol(w http.ResponseWriter, r *http.Request) {
	comp := strings.HasPrefix(r.URL.Path, "/export/competition")
	sheets, base, err := a.collectProtocol(r, comp)
	if err != nil {
		exportFailed(w, r, err)
		return
	}
	doc := protocolDoc{
		filename: pdfFileName(base) + ".pdf",
		csvName:  pdfFileName(base) + ".csv",
		sheets:   sheets,
		csv:      resultsCSV(sheets),
	}
	doc.title = "Ergebnisprotokoll " + sheets[0].label
	if len(sheets) > 1 {
		doc.title = fmt.Sprintf("Ergebnisprotokoll %s (%d Disziplinen)", base, len(sheets))
	}

	buf := new(bytes.Buffer)
	if err := protocolPDF(&doc, buf); err != nil {
		http.Error(w, "Export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+doc.filename+`"`)
	w.Write(buf.Bytes())
}

// exportResultsCSV liefert dieselben Daten wie die PDF-Routen, nur als Datei
// (am Handy kommt man an einen PDF-Anhang sonst nicht heran).
func (a *App) exportResultsCSV(w http.ResponseWriter, r *http.Request) {
	comp := strings.HasPrefix(r.URL.Path, "/export/competition")
	sheets, base, err := a.collectProtocol(r, comp)
	if err != nil {
		exportFailed(w, r, err)
		return
	}
	data := resultsCSV(sheets)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+pdfFileName(base)+`.csv"`)
	w.Write(data)
}

// exportFailed ist die Fehlerbehandlung beider Export-Routen.
func exportFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNoView) {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "Export: "+err.Error(), http.StatusInternalServerError)
}

// ---------- Daten ----------

// collectProtocol sammelt die Blätter für den Export. Disziplinen ohne
// komplette Wertung werden übersprungen (und im ersten Blatt als Hinweis
// genannt) – das spart Papier, wenn im Turnier einiges noch läuft.
func (a *App) collectProtocol(r *http.Request, comp bool) ([]protocolSheet, string, error) {
	now := time.Now()
	_, compID, discID := a.resolveView(r)

	if !comp {
		if discID == 0 {
			return nil, "", errNoView
		}
		ld, err := a.loadLive(discID, now)
		if err != nil {
			return nil, "", err
		}
		return []protocolSheet{sheetFromLive(ld)}, ld.Disc.Name, nil
	}

	if compID == 0 {
		return nil, "", errNoView
	}
	turnier, err := a.store.GetCompetition(compID)
	if err != nil {
		return nil, "", errNoView
	}
	discs, err := a.store.ListDisciplines(compID)
	if err != nil {
		return nil, "", err
	}
	var sheets []protocolSheet
	var empty []string
	for _, d := range discs {
		ld, err := a.loadLive(d.ID, now)
		if err != nil {
			return nil, "", err
		}
		sh := sheetFromLive(ld)
		if len(sh.rows) == 0 {
			empty = append(empty, d.Name)
			continue
		}
		sheets = append(sheets, sh)
	}
	if len(sheets) == 0 {
		// weiter mit einem leeren Blatt: der Knopf soll nie ins Leere führen
		sheets = append(sheets, protocolSheet{
			disc:  turnier.Name,
			label: turnier.Name,
			meta: "keine Disziplin mit kompletter Wertung · Stand: " +
				time.Unix(now.Unix(), 0).In(schedule.Zone).Format("02.01.2006, 15:04"),
			numJ: 7,
		})
	} else if len(empty) > 0 {
		sheets[0].note = "ohne komplette Wertung: " + strings.Join(empty, ", ")
	}
	return sheets, turnier.Name, nil
}

// protocolRows baut den Wertungsbogen: komplett bewertete Tänze in
// Startreihenfolge (nicht in Platzordnung – das ist die Heft-Logik).
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
		rows = append(rows, protocolRow{
			startNr: p.StartNr, name: p.Name, verein: p.Verein, star: starred[p.ID],
			scores:   append([]int(nil), row.Values...),
			lowIdx:   row.LowIdx,
			highIdx:  row.HighIdx,
			total:    row.Total,
			official: row.Official,
			rank:     row.Rank,
		})
	}
	return rows
}

// sheetFromLive macht aus dem Seiten-Datensatz ein Protokollblatt.
func sheetFromLive(ld *LiveData) protocolSheet {
	done := len(ld.Plain) + len(ld.Star)
	total := ld.ActivePlain + ld.ActiveStar
	meta := joinBits(
		fmt.Sprintf("%d von %d Tänzern fertig bewertet", done, total),
		"Stand: "+time.Unix(ld.NowUnix, 0).In(schedule.Zone).Format("02.01.2006, 15:04"),
	)
	return protocolSheet{
		disc:  ld.Disc.Name,
		label: joinBits(ld.Disc.CompetitionName, ld.Disc.Name),
		meta:  joinBits(ld.Disc.SessionName, meta),
		numJ:  ld.NumJ,
		rows:  protocolRows(ld),
	}
}

// ---------- PDF ----------

// protocolPDF schreibt das Protokoll als Querformat-A4 nach w, inklusive
// CSV-Anhang (Dokument-Anhang nach PDF-Standard).
func protocolPDF(doc *protocolDoc, w io.Writer) error {
	usableW := pdfPageW - 2*pdfMargin
	pdf := fpdf.New("L", "mm", "A4", "")
	if err := pdf.Error(); err != nil {
		return err
	}
	pdf.SetCompression(false)
	pdf.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	// Eigene Umbrüche: damit auf jeder Seite der Spaltenkopf steht.
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetTitle(to1252(doc.title), false)
	pdf.AliasNbPages("{nb}")

	if len(doc.csv) > 0 {
		pdf.SetAttachments([]fpdf.Attachment{{
			Content:     doc.csv,
			Filename:    doc.csvName,
			Description: "Ergebnisse als CSV (Semikolon getrennt)",
		}})
	}

	// Fußzeile: auf einem losen Ausdruck muss stehen, worauf er sich bezieht.
	foot := ""
	pdf.SetFooterFunc(func() {
		pdf.SetXY(pdfMargin, pdfBodyBottom+1.5)
		pdf.SetFont("Helvetica", "", 6.5)
		pdf.SetTextColor(115, 115, 115)
		txt := foot + " · Seite " + strconv.Itoa(pdf.PageNo()) + " von {nb}"
		pdf.Cell(usableW, 4.5, fit1252(pdf, txt, usableW))
		pdf.SetTextColor(0, 0, 0)
	})

	for _, sh := range doc.sheets {
		foot = sh.label
		pdf.AddPage()
		y := drawSheetHead(pdf, sh)
		if len(sh.rows) == 0 {
			pdf.SetFont("Helvetica", "", 10)
			pdf.SetXY(pdfMargin, y+2)
			pdf.Cell(usableW, 6, to1252("Noch keine komplette Wertung – das Protokoll füllt sich, sobald alle Noten erfasst sind."))
			continue
		}
		drawRows(pdf, sh, y)
	}
	return finishPDF(pdf, w)
}

// drawSheetHead zeichnet Kopf (Titel, Herkunft, Lesehilfe) und Spaltenkopf
// des aktuellen Blatts und gibt die y-Position der ersten Datenzeile zurück.
func drawSheetHead(pdf *fpdf.Fpdf, sh protocolSheet) float64 {
	usableW := pdfPageW - 2*pdfMargin

	pdf.SetFont("Helvetica", "B", 14)
	pdf.SetXY(pdfMargin, pdfMargin+1)
	pdf.Cell(usableW, 8, fit1252(pdf, "Ergebnisprotokoll – "+sh.label, usableW))

	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetXY(pdfMargin, pdfMargin+11.5)
	pdf.Cell(usableW, 5, to1252(sh.meta))

	legend := "Klammern = Note zählt nicht zur Summe (Streichregel) · * = offiziell genannte Summe · (S) = Sternchen · alle Werte auch als CSV-Anhang im PDF"
	pdf.SetFont("Helvetica", "I", 7)
	pdf.SetXY(pdfMargin, pdfMargin+17.5)
	pdf.Cell(usableW, 4, to1252(legend))

	y := pdfMargin + 23
	if sh.note != "" {
		pdf.SetFont("Helvetica", "I", 7)
		pdf.SetXY(pdfMargin, pdfMargin+22)
		pdf.Cell(usableW, 4, fit1252(pdf, sh.note, usableW))
		y = pdfMargin + 27
	}
	pdf.SetDrawColor(120, 120, 120)
	pdf.Line(pdfMargin, y, pdfPageW-pdfMargin, y)

	if len(sh.rows) == 0 {
		return y + 5 // Hinweistext-Seite: ohne leere Tabellenspalten
	}
	return drawColsHead(pdf, sh, y+3) + 2.5
}

// drawRows schreibt die Tänze des Blatts; eine neue Seite bekommt nur den
// Spaltenkopf (mit kleinem Disziplinhinweis), keinen kompletten Kopf.
func drawRows(pdf *fpdf.Fpdf, sh protocolSheet, y float64) {
	usableW := pdfPageW - 2*pdfMargin
	nrW, nameW, vereinW, scoreW, sumW, platzW := pdfColumnWidths(sh.numJ)
	for _, row := range sh.rows {
		if y+pdfRowH > pdfBodyBottom {
			pdf.AddPage()
			pdf.SetFont("Helvetica", "B", 9)
			pdf.SetXY(pdfMargin, pdfMargin+1)
			pdf.SetTextColor(90, 90, 90)
			pdf.Cell(usableW, 5, fit1252(pdf, sh.label+" (Fortsetzung)", usableW))
			pdf.SetTextColor(0, 0, 0)
			y = drawColsHead(pdf, sh, pdfMargin+8) + 2.5
		}
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetXY(pdfMargin, y)
		name := row.name
		if row.star {
			name = "(S) " + name
		}
		pdf.CellFormat(nrW, pdfRowH, to1252(row.startNr), "", 0, "C", false, 0, "")
		pdf.CellFormat(nameW, pdfRowH, fit1252(pdf, name, nameW-1.5), "", 0, "L", false, 0, "")
		pdf.CellFormat(vereinW, pdfRowH, fit1252(pdf, row.verein, vereinW-1.5), "", 0, "L", false, 0, "")
		for _, sc := range scoreCells(row) {
			pdf.CellFormat(scoreW, pdfRowH, to1252(sc), "", 0, "C", false, 0, "")
		}
		total := strconv.Itoa(row.total)
		if row.official {
			total += " *"
		}
		pdf.CellFormat(sumW, pdfRowH, to1252(total), "", 0, "C", false, 0, "")
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
}

// drawColsHead zeichnet den Spaltenkopf (inkl. "R1..Rn") und gibt die
// y-Position der Trennlinie darunter zurück.
func drawColsHead(pdf *fpdf.Fpdf, sh protocolSheet, y float64) float64 {
	nrW, nameW, vereinW, scoreW, sumW, platzW := pdfColumnWidths(sh.numJ)
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetXY(pdfMargin, y)
	pdf.CellFormat(nrW, pdfRowH, "Start", "", 0, "C", false, 0, "")
	pdf.CellFormat(nameW, pdfRowH, "Name", "", 0, "L", false, 0, "")
	pdf.CellFormat(vereinW, pdfRowH, "Verein", "", 0, "L", false, 0, "")
	for j := 1; j <= sh.numJ; j++ {
		pdf.CellFormat(scoreW, pdfRowH, "R"+strconv.Itoa(j), "", 0, "C", false, 0, "")
	}
	pdf.CellFormat(sumW, pdfRowH, "Gesamt", "", 0, "C", false, 0, "")
	pdf.CellFormat(platzW, pdfRowH, "Platz", "", 0, "C", false, 0, "")
	pdf.SetDrawColor(120, 120, 120)
	y2 := y + pdfRowH + 0.8
	pdf.Line(pdfMargin, y2, pdfPageW-pdfMargin, y2)
	return y2
}

func finishPDF(pdf *fpdf.Fpdf, w io.Writer) error {
	if err := pdf.Error(); err != nil {
		return err
	}
	return pdf.Output(w)
}

// scoreCells formatiert die Noten für die Tabelle: fehlend als "-",
// gestrichene in Klammern.
func scoreCells(row protocolRow) []string {
	out := make([]string, 0, len(row.scores))
	for i, v := range row.scores {
		switch {
		case v < 0:
			out = append(out, "-")
		case i == row.lowIdx || i == row.highIdx:
			out = append(out, "("+strconv.Itoa(v)+")")
		default:
			out = append(out, strconv.Itoa(v))
		}
	}
	return out
}

// ---------- CSV ----------

// resultsCSV baut dieselben Werte als CSV: Semikolon wie beim Admin-CSV,
// UTF-8 mit BOM (damit Excel die Umlaute richtig liest). Eine Zeile je
// komplett bewertetem Tanz, Notenspalten bis zur höchsten Richterzahl der
// beteiligten Disziplinen (fehlende Noten bleiben leer).
func resultsCSV(sheets []protocolSheet) []byte {
	numJ := 1
	for _, sh := range sheets {
		if sh.numJ > numJ {
			numJ = sh.numJ
		}
	}
	var b bytes.Buffer
	b.WriteString("\ufeff")
	head := []string{"disziplin", "startnr", "name", "verein", "stern"}
	for j := 1; j <= numJ; j++ {
		head = append(head, "R"+strconv.Itoa(j))
	}
	head = append(head, "gesamt", "platz", "anmerkung")
	b.WriteString(strings.Join(head, ";"))
	b.WriteString("\n")
	for _, sh := range sheets {
		for _, row := range sh.rows {
			star := ""
			if row.star {
				star = "x"
			}
			fields := []string{sh.disc, row.startNr, row.name, row.verein, star}
			for i := 0; i < numJ; i++ {
				if i < len(row.scores) && row.scores[i] >= 0 {
					fields = append(fields, strconv.Itoa(row.scores[i]))
				} else {
					fields = append(fields, "")
				}
			}
			rank := ""
			if row.rank > 0 {
				rank = strconv.Itoa(row.rank)
			}
			fields = append(fields, strconv.Itoa(row.total), rank, dropNote(row))
			b.WriteString(strings.Join(fields, ";"))
			b.WriteString("\n")
		}
	}
	return b.Bytes()
}

// dropNote beschreibt, was an der Summe nicht standardmäßig ist: die
// gestrichenen Richter und eine offiziell genannte Summe.
func dropNote(row protocolRow) string {
	var drop []string
	for i := range row.scores {
		if i == row.lowIdx || i == row.highIdx {
			drop = append(drop, "R"+strconv.Itoa(i+1))
		}
	}
	parts := make([]string, 0, 2)
	if len(drop) > 0 {
		parts = append(parts, "gestrichen: "+strings.Join(drop, ","))
	}
	if row.official {
		parts = append(parts, "Summe offiziell")
	}
	return strings.Join(parts, ", ")
}

// ---------- Helfer ----------

// joinBits hängt nicht-leere Teile mit " · " aneinander.
func joinBits(parts ...string) string {
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
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
		cand := to1252(strings.TrimRight(string(r), " ") + "…")
		if pdf.GetStringWidth(cand) <= w {
			return cand
		}
	}
	return to1252("…")
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

// pdfFileName macht aus dem Disziplinen-/Turnier-Namen einen sicheren
// Dateinamen (ohne Endung): Umlaut-Buchstaben werden umgeschrieben, alles
// außer Buchstaben, Ziffern und Bindestrich fällt weg (leere Reste zu
// "export").
func pdfFileName(name string) string {
	s := strings.NewReplacer(
		"Ä", "Ae", "Ö", "Oe", "Ü", "Ue",
		"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	).Replace(name)
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
	return out
}
