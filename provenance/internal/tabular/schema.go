package tabular

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Field is a column that tolerates a change in its last digit, and so may
// carry low-order-bit marks. Nothing else is ever altered.
type Field struct {
	Name      string `json:"name"`
	Decimals  int    `json:"decimals,omitempty"`
	Timestamp bool   `json:"timestamp,omitempty"`
	Tolerance string `json:"tolerance"`
}

// Schema says how a table may be marked: which column identifies a row, which
// other columns can match a row back to the source if that column is dropped,
// and which columns tolerate a low-order change.
type Schema struct {
	Key      string   `json:"key"`
	Match    []string `json:"match"`
	Tolerant []Field  `json:"tolerant"`
	Redact   []string `json:"redact"` // personal columns the redaction measure replaces
	Dummy    string   `json:"dummy"`
}

// Column is what profiling found about one column, shown in the UI so the
// data owner can confirm or correct the suggestion.
type Column struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`               // text | integer | decimal | timestamp | date
	Personal  bool    `json:"personal,omitempty"` // reads as a name, contact detail or address
	Unique    float64 `json:"unique"`
	Decimals  int     `json:"decimals,omitempty"`
	Sample    string  `json:"sample"`
	Tolerance string  `json:"tolerance,omitempty"` // suggested, empty if not tolerant
}

var (
	reTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}\.(\d{1,6})`)
	reDecimal   = regexp.MustCompile(`^-?\d+\.(\d+)$`)
	reInteger   = regexp.MustCompile(`^-?\d+$`)
	reDigits    = regexp.MustCompile(`\d+`)
	reDate      = regexp.MustCompile(`^(?:\d{4}-\d{1,2}-\d{1,2}(?:[ T]\d{1,2}:\d{2}(?::\d{2})?(?:Z|[+-]\d{2}:?\d{2})?)?|\d{1,2}[./-]\d{1,2}[./-]\d{2,4}(?: \d{1,2}:\d{2}(?::\d{2})?)?)$`)
	reEmail     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[A-Za-z]{2,}$`)
	reWordSplit = regexp.MustCompile(`[^\p{L}]+`)
)

// personalWords are header words that name a person or their contact
// details. They decide which columns redaction is suggested for; the data
// owner confirms or changes that before anything is marked.
var personalWords = map[string]bool{
	"name": true, "names": true, "firstname": true, "lastname": true, "surname": true, "fullname": true,
	"first": true, "last": true, "given": true, "family": true, "forename": true, "middle": true,
	"vorname": true, "nachname": true, "prenom": true, "prénom": true, "nom": true, "cognome": true, "nome": true,
	"email": true, "mail": true, "phone": true, "telephone": true, "tel": true, "mobile": true, "telefon": true,
	"handy": true, "natel": true, "address": true, "adresse": true, "street": true, "strasse": true, "straße": true,
	"rue": true, "iban": true, "ssn": true, "ahv": true, "passport": true, "contact": true, "kontakt": true,
	"person": true, "patient": true, "customer": false,
}

// notPersonal are header words that make a "name" column something other
// than a person's name: a company, a product, a place.
var notPersonal = map[string]bool{
	"company": true, "firma": true, "firm": true, "organisation": true, "organization": true, "org": true,
	"product": true, "produkt": true, "city": true, "ort": true, "country": true, "land": true, "file": true,
	"brand": true, "bank": true, "store": true, "shop": true, "branch": true, "filiale": true, "user": false,
}

// personalHeader reports whether a column header names personal data.
func personalHeader(name string) bool {
	words := reWordSplit.Split(strings.ToLower(name), -1)
	joined := strings.Join(words, "")
	hit := personalWords[joined]
	for _, w := range words {
		if notPersonal[w] {
			return false
		}
		if personalWords[w] {
			hit = true
		}
	}
	return hit
}

// mode returns the most common value of a small integer tally.
func mode(counts map[int]int) (val, n int) {
	for v, c := range counts {
		if c > n || (c == n && v < val) {
			val, n = v, c
		}
	}
	return
}

// Profile describes every column of a table.
func Profile(t *Table) []Column {
	out := make([]Column, len(t.Columns))
	for i, name := range t.Columns {
		c := Column{Name: name, Kind: "text"}
		seen := map[string]bool{}
		decimals, fracs := map[int]int{}, map[int]int{}
		ints, dates, emails, nonEmpty := 0, 0, 0, 0
		for _, row := range t.Rows {
			v := row[i]
			if v == "" {
				continue
			}
			nonEmpty++
			seen[v] = true
			if c.Sample == "" {
				c.Sample = v
			}
			switch {
			case reTimestamp.MatchString(v):
				fracs[len(reTimestamp.FindStringSubmatch(v)[1])]++
			case reDecimal.MatchString(v):
				decimals[len(reDecimal.FindStringSubmatch(v)[1])]++
			case reInteger.MatchString(v):
				ints++
			case reDate.MatchString(v):
				dates++
			case reEmail.MatchString(v):
				emails++
			}
		}
		if nonEmpty == 0 {
			out[i] = c
			continue
		}
		c.Unique = float64(len(seen)) / float64(nonEmpty)
		switch d, n := mode(decimals); {
		case len(fracs) > 0:
			if f, fn := mode(fracs); fn*10 >= nonEmpty*9 {
				c.Kind, c.Decimals = "timestamp", f
				if f >= 3 {
					c.Tolerance = "1 ms"
				}
			}
		case n*10 >= nonEmpty*9:
			c.Kind, c.Decimals = "decimal", d
			if d >= 3 {
				c.Tolerance = "0." + strings.Repeat("0", d-1) + "1"
			}
		case ints*10 >= nonEmpty*9:
			c.Kind = "integer"
		case dates*10 >= nonEmpty*9:
			c.Kind = "date"
		}
		c.Personal = c.Kind == "text" && (personalHeader(name) || emails*10 >= nonEmpty*8)
		out[i] = c
	}
	return out
}

// DetectSchema suggests a marking schema for a table.
func DetectSchema(t *Table) (Schema, []Column) {
	cols := Profile(t)
	s := Schema{Dummy: freeColumnName(t, "ref_code")}

	// Identifiers are text or whole-number columns whose values are distinct.
	// Measurements are not identifiers even when every value happens to differ.
	identifier := func(c Column) bool {
		return (c.Kind == "text" || c.Kind == "integer") && c.Tolerance == ""
	}
	bestScore := -1.0
	for _, c := range cols {
		if c.Unique < 1 || !identifier(c) {
			continue
		}
		score := 1.0
		if n := strings.ToLower(c.Name); strings.Contains(n, "id") || strings.Contains(n, "ref") ||
			strings.Contains(n, "key") || strings.Contains(n, "number") || strings.Contains(n, "code") {
			score += 2
		}
		if score > bestScore {
			s.Key, bestScore = c.Name, score
		}
	}
	for _, c := range cols {
		if c.Name != s.Key && c.Unique >= 0.98 && identifier(c) && !c.Personal && len(s.Match) < 3 {
			s.Match = append(s.Match, c.Name)
		}
	}
	for _, c := range cols {
		if c.Personal && c.Name != s.Key {
			s.Redact = append(s.Redact, c.Name)
		}
	}
	for _, c := range cols {
		if c.Tolerance == "" || len(s.Tolerant) >= 6 {
			continue
		}
		s.Tolerant = append(s.Tolerant, Field{
			Name: c.Name, Decimals: c.Decimals, Timestamp: c.Kind == "timestamp", Tolerance: c.Tolerance,
		})
	}
	return s, cols
}

func freeColumnName(t *Table, want string) string {
	for n, name := 1, want; ; n++ {
		if t.Col(name) < 0 {
			return name
		}
		name = fmt.Sprintf("%s_%d", want, n)
	}
}

// Validate checks a schema against the table it will mark.
func (s Schema) Validate(t *Table) error {
	if s.Key != "" && t.Col(s.Key) < 0 {
		return fmt.Errorf("column %q is not in the table", s.Key)
	}
	for _, m := range s.Match {
		if t.Col(m) < 0 {
			return fmt.Errorf("column %q is not in the table", m)
		}
	}
	for _, f := range s.Tolerant {
		c := t.Col(f.Name)
		if c < 0 {
			return fmt.Errorf("column %q is not in the table", f.Name)
		}
		if !f.Timestamp && f.Decimals < 1 {
			return fmt.Errorf("column %q needs at least one decimal place to carry a mark", f.Name)
		}
		for _, row := range t.Rows {
			if _, ok := readParity(f, row[c]); ok {
				break
			}
		}
	}
	for _, name := range s.Redact {
		switch {
		case t.Col(name) < 0:
			return fmt.Errorf("column %q is not in the table", name)
		case name == s.Key:
			return fmt.Errorf("column %q identifies rows, so it cannot also be redacted", name)
		case s.tolerant(name):
			return fmt.Errorf("column %q cannot be both tolerant and redacted", name)
		}
		for _, m := range s.Match {
			if m == name {
				return fmt.Errorf("column %q cannot both match rows and be redacted", name)
			}
		}
	}
	if s.Key != "" && s.tolerant(s.Key) {
		return fmt.Errorf("column %q identifies rows, so it cannot also carry marks", s.Key)
	}
	if s.Key == "" && len(s.Match) == 0 && len(s.stable(t)) == 0 {
		return fmt.Errorf("no column identifies a row, so marks cannot be placed")
	}
	return nil
}

// stable lists the columns that marking never alters, which is what a row is
// recognised by when its identifying column has been dropped. Redacted
// columns are left out: they differ from copy to copy.
func (s Schema) stable(t *Table) []string {
	var out []string
	for _, name := range t.Columns {
		if name == s.Dummy || s.tolerant(name) || s.redacted(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func (s Schema) redacted(name string) bool {
	for _, r := range s.Redact {
		if r == name {
			return true
		}
	}
	return false
}

func (s Schema) tolerant(name string) bool {
	for _, f := range s.Tolerant {
		if f.Name == name {
			return true
		}
	}
	return false
}

// rowKey returns the value that ties a row to its marks. With no identifying
// column it falls back to a hash of the columns that marking leaves alone.
func (s Schema) rowKey(t *Table, row []string) string {
	if s.Key != "" {
		if c := t.Col(s.Key); c >= 0 {
			return row[c]
		}
	}
	var parts []string
	for _, name := range s.stable(t) {
		parts = append(parts, row[t.Col(name)])
	}
	return "h:" + strings.Join(parts, "\x00")
}

// Describe summarises a schema for the issuance log.
func (s Schema) Describe() string {
	key := s.Key
	if key == "" {
		key = "row contents"
	}
	names := make([]string, 0, len(s.Tolerant))
	for _, f := range s.Tolerant {
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return "identified by " + key + ", no tolerant columns"
	}
	return "identified by " + key + ", tolerant: " + strings.Join(names, ", ")
}

// mutateValue rerolls the digits of a value, keeping its shape, so canary
// values look like the real ones without copying any of them.
func mutateValue(v string, rnd func(n int) int) string {
	locs := reDigits.FindAllStringIndex(v, -1)
	if len(locs) == 0 {
		return v
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		b.WriteString(v[prev:loc[0]])
		for i := loc[0]; i < loc[1]; i++ {
			b.WriteString(strconv.Itoa(rnd(10)))
		}
		prev = loc[1]
	}
	b.WriteString(v[prev:])
	return b.String()
}
