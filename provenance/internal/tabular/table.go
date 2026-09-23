// Package tabular implements Module A: marking a structured dataset per
// recipient (canary rows, low-order-bit masking, dummy column), simulating
// leaks, and attributing a recovered copy.
package tabular

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
)

type Table struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Format is how the file was written, so a marked copy goes back out the
	// way the source came in. Values are held with a decimal point either way.
	Format Format `json:"format"`
}

// Format describes a CSV dialect.
type Format struct {
	Delimiter    string `json:"delimiter"`    // "," ";" "\t" or "|"
	DecimalComma bool   `json:"decimalComma"` // numbers written 7951,14
	BOM          bool   `json:"bom"`
}

func (f Format) delim() rune {
	if f.Delimiter == "" {
		return ','
	}
	return []rune(f.Delimiter)[0]
}

func (t *Table) Col(name string) int {
	for i, c := range t.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

func (t *Table) Clone() *Table {
	c := &Table{Columns: append([]string(nil), t.Columns...), Rows: make([][]string, len(t.Rows)), Format: t.Format}
	for i, r := range t.Rows {
		c.Rows[i] = append([]string(nil), r...)
	}
	return c
}

func (t *Table) Preview(n int) *Table {
	if n > len(t.Rows) {
		n = len(t.Rows)
	}
	return &Table{Columns: t.Columns, Rows: t.Rows[:n], Format: t.Format}
}

// fingerprint identifies the table's content, independent of its dialect.
func (t *Table) fingerprint() string {
	h := sha256.New()
	for _, c := range t.Columns {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	for _, row := range t.Rows {
		for _, v := range row {
			h.Write([]byte(v))
			h.Write([]byte{0})
		}
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// CSV writes the table in its own dialect.
func (t *Table) CSV() []byte {
	var b bytes.Buffer
	if t.Format.BOM {
		b.WriteString("\uFEFF")
	}
	w := csv.NewWriter(&b)
	w.Comma = t.Format.delim()
	w.Write(t.Columns)
	for _, row := range t.Rows {
		if t.Format.DecimalComma {
			out := make([]string, len(row))
			for i, v := range row {
				out[i] = v
				if reDecimal.MatchString(v) {
					out[i] = strings.Replace(v, ".", ",", 1)
				}
			}
			row = out
		}
		w.Write(row)
	}
	w.Flush()
	return b.Bytes()
}

// ParseCSV reads a CSV file, working out its dialect: the delimiter, a byte
// order mark, and decimal commas as Excel writes them in Switzerland and
// Germany.
func ParseCSV(r io.Reader) (*Table, error) {
	return ParseCSVWith(r, Format{})
}

// ParseCSVWith reads a CSV file. Any field of want that is set overrides what
// would be detected; DecimalComma is detected unless want.Delimiter is set.
func ParseCSVWith(r io.Reader, want Format) (*Table, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	f := Format{}
	if bytes.HasPrefix(data, []byte("\uFEFF")) {
		f.BOM = true
		data = data[3:]
	}
	f.Delimiter = want.Delimiter
	if f.Delimiter == "" {
		f.Delimiter = sniffDelimiter(data)
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = f.delim()
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) < 2 {
		return nil, fmt.Errorf("CSV needs a header row and at least one data row")
	}
	t := &Table{Columns: recs[0]}
	for i := range t.Columns {
		t.Columns[i] = strings.TrimSpace(strings.TrimPrefix(t.Columns[i], "\uFEFF"))
	}
	for _, r := range recs[1:] {
		if len(r) == 1 && strings.TrimSpace(r[0]) == "" {
			continue // a blank line
		}
		row := make([]string, len(t.Columns))
		for i := range row {
			if i < len(r) {
				row[i] = strings.TrimSpace(r[i])
			}
		}
		t.Rows = append(t.Rows, row)
	}
	f.DecimalComma = want.DecimalComma
	if want.Delimiter == "" && !want.DecimalComma {
		f.DecimalComma = f.Delimiter != "," && usesDecimalComma(t)
	}
	if f.DecimalComma {
		for _, row := range t.Rows {
			for i, v := range row {
				if n, ok := commaNumber(v); ok {
					row[i] = n
				}
			}
		}
	}
	t.Format = f
	return t, nil
}

var (
	reCommaDecimal = regexp.MustCompile(`^-?\d+,\d+$`)
	reGroupedComma = regexp.MustCompile(`^-?\d{1,3}(?:[.'’]\d{3})+,\d+$`)
)

// commaNumber converts 7951,14 or 7.951,14 to 7951.14.
func commaNumber(v string) (string, bool) {
	switch {
	case reCommaDecimal.MatchString(v):
		return strings.Replace(v, ",", ".", 1), true
	case reGroupedComma.MatchString(v):
		v = strings.NewReplacer(".", "", "'", "", "’", "").Replace(v)
		return strings.Replace(v, ",", ".", 1), true
	}
	return v, false
}

// usesDecimalComma reports whether some column is mostly numbers written with
// a decimal comma.
func usesDecimalComma(t *Table) bool {
	for c := range t.Columns {
		comma, filled := 0, 0
		for _, row := range t.Rows {
			if row[c] == "" {
				continue
			}
			filled++
			if _, ok := commaNumber(row[c]); ok {
				comma++
			}
		}
		if filled > 0 && comma*10 >= filled*8 {
			return true
		}
	}
	return false
}

// sniffDelimiter picks the candidate that splits the first lines into the
// same number of fields most consistently, ignoring anything inside quotes.
func sniffDelimiter(data []byte) string {
	lines := splitRecords(data, 30)
	best, bestScore := ",", -1.0
	for _, d := range []byte{',', ';', '\t', '|'} {
		counts := map[int]int{}
		for _, l := range lines {
			counts[countOutsideQuotes(l, d)]++
		}
		mode, n := 0, 0
		for c, k := range counts {
			if k > n || (k == n && c > mode) {
				mode, n = c, k
			}
		}
		if mode == 0 {
			continue
		}
		score := float64(n)/float64(len(lines))*10 + float64(mode)/100
		if score > bestScore {
			best, bestScore = string(d), score
		}
	}
	return best
}

// splitRecords returns up to n records' raw text, keeping quoted newlines
// inside their record.
func splitRecords(data []byte, n int) [][]byte {
	var out [][]byte
	start, quoted := 0, false
	for i := 0; i < len(data) && len(out) < n; i++ {
		switch data[i] {
		case '"':
			quoted = !quoted
		case '\n':
			if !quoted {
				if line := bytes.TrimRight(data[start:i], "\r"); len(line) > 0 {
					out = append(out, line)
				}
				start = i + 1
			}
		}
	}
	if len(out) < n && start < len(data) {
		if line := bytes.TrimSpace(data[start:]); len(line) > 0 {
			out = append(out, line)
		}
	}
	return out
}

func countOutsideQuotes(line []byte, d byte) int {
	n, quoted := 0, false
	for _, c := range line {
		switch {
		case c == '"':
			quoted = !quoted
		case c == d && !quoted:
			n++
		}
	}
	return n
}

var Columns = []string{"account_id", "full_name", "email", "city", "opened_at", "balance_eur", "latitude", "longitude", "risk_score"}

const tsLayout = "2006-01-02 15:04:05.000"

var (
	firstNames = []string{"Anna", "Luca", "Mia", "Noah", "Lea", "Elias", "Sofia", "Leon", "Emma", "David", "Laura", "Julian", "Sara", "Marco", "Nina", "Jonas", "Chiara", "Samuel", "Elena", "Fabian", "Alina", "Nicolas", "Lara", "Simon", "Jana", "Matteo", "Céline", "Adrian", "Olivia", "Tim", "Vanessa", "Patrick", "Jasmin", "Raphael", "Carla", "Daniel", "Petra", "Yannick", "Irina", "Thomas"}
	lastNames  = []string{"Müller", "Meier", "Schmid", "Keller", "Weber", "Huber", "Schneider", "Meyer", "Steiner", "Fischer", "Gerber", "Brunner", "Baumann", "Frei", "Zimmermann", "Moser", "Widmer", "Wyss", "Graf", "Roth", "Rossi", "Bianchi", "Favre", "Rochat", "Bonvin", "Kälin", "Suter", "Lehmann", "Bühler", "Marti", "Egli", "Hofer", "Vogel", "Kaufmann", "Bachmann", "Sutter", "Pfister", "Ammann", "Koch", "Gasser"}
	domains    = []string{"bluewin.ch", "gmail.com", "gmx.ch", "outlook.com", "proton.me", "sunrise.ch", "hotmail.com", "icloud.com"}
	cities     = []struct {
		Name     string
		Lat, Lon float64
	}{
		{"Zürich", 47.3769, 8.5417}, {"Genève", 46.2044, 6.1432}, {"Basel", 47.5596, 7.5886},
		{"Bern", 46.9480, 7.4474}, {"Lausanne", 46.5197, 6.6323}, {"Lugano", 46.0037, 8.9511},
		{"Luzern", 47.0502, 8.3093}, {"St. Gallen", 47.4245, 9.3767}, {"Winterthur", 47.4999, 8.7262},
		{"Fribourg", 46.8065, 7.1620},
	}
)

func asciiFold(s string) string {
	r := strings.NewReplacer("ü", "ue", "ö", "oe", "ä", "ae", "é", "e", "è", "e", "ô", "o")
	return r.Replace(s)
}

// genRow draws one plausible account. Real rows and canary rows use the same
// generator so canaries are statistically indistinguishable.
func genRow(rng *rand.Rand, usedIDs, usedEmails map[string]bool) []string {
	var id string
	for {
		id = fmt.Sprintf("AC-%06d", 100000+rng.IntN(900000))
		if !usedIDs[id] {
			usedIDs[id] = true
			break
		}
	}
	first := firstNames[rng.IntN(len(firstNames))]
	last := lastNames[rng.IntN(len(lastNames))]
	base := strings.ToLower(asciiFold(first) + "." + asciiFold(last))
	domain := domains[rng.IntN(len(domains))]
	email := base + "@" + domain
	for n := 2; usedEmails[email]; n++ {
		email = fmt.Sprintf("%s%d@%s", base, n+rng.IntN(90), domain)
	}
	usedEmails[email] = true
	c := cities[rng.IntN(len(cities))]
	start := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	opened := start.Add(time.Duration(rng.Int64N(int64(10*365*24*time.Hour/time.Millisecond))) * time.Millisecond)
	balance := math.Exp(rng.NormFloat64()*1.3 + 8.5)
	risk := math.Pow(rng.Float64(), 2.2)
	return []string{
		id,
		first + " " + last,
		email,
		c.Name,
		opened.Format(tsLayout),
		fmt.Sprintf("%.2f", balance),
		formatFixed(int64(math.Round((c.Lat+rng.NormFloat64()*0.02)*1e6)), 6),
		formatFixed(int64(math.Round((c.Lon+rng.NormFloat64()*0.03)*1e6)), 6),
		formatFixed(int64(math.Round(risk*1e4)), 4),
	}
}

// Generate builds the synthetic source table.
func Generate(n int, seed uint64) *Table {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	usedIDs, usedEmails := map[string]bool{}, map[string]bool{}
	t := &Table{Columns: append([]string(nil), Columns...)}
	for i := 0; i < n; i++ {
		t.Rows = append(t.Rows, genRow(rng, usedIDs, usedEmails))
	}
	return t
}

// formatFixed prints v / 10^dec with exactly dec decimals, without float error.
func formatFixed(v int64, dec int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	p := int64(math.Pow10(dec))
	return fmt.Sprintf("%s%d.%0*d", sign, v/p, dec, v%p)
}
