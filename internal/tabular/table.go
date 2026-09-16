// Package tabular implements Module A: marking a structured dataset per
// recipient (canary rows, low-order-bit masking, dummy column), simulating
// leaks, and attributing a recovered copy.
package tabular

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

type Table struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
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
	c := &Table{Columns: append([]string(nil), t.Columns...), Rows: make([][]string, len(t.Rows))}
	for i, r := range t.Rows {
		c.Rows[i] = append([]string(nil), r...)
	}
	return c
}

func (t *Table) Preview(n int) *Table {
	if n > len(t.Rows) {
		n = len(t.Rows)
	}
	return &Table{Columns: t.Columns, Rows: t.Rows[:n]}
}

func (t *Table) CSV() []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write(t.Columns)
	w.WriteAll(t.Rows)
	return b.Bytes()
}

func ParseCSV(r io.Reader) (*Table, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
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
		row := make([]string, len(t.Columns))
		for i := range row {
			if i < len(r) {
				row[i] = strings.TrimSpace(r[i])
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
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
