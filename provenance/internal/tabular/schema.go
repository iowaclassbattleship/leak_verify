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
	Dummy    string   `json:"dummy"`
}

// Column is what profiling found about one column, shown in the UI so the
// data owner can confirm or correct the suggestion.
type Column struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"` // text | integer | decimal | timestamp
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
)

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
		ints, nonEmpty := 0, 0
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
		}
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
		if c.Name != s.Key && c.Unique >= 0.98 && identifier(c) && len(s.Match) < 3 {
			s.Match = append(s.Match, c.Name)
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
	if s.Key == "" && len(s.Match) == 0 && len(s.stable(t)) == 0 {
		return fmt.Errorf("no column identifies a row, so marks cannot be placed")
	}
	return nil
}

// stable lists the columns that marking never alters, which is what a row is
// recognised by when its identifying column has been dropped.
func (s Schema) stable(t *Table) []string {
	var out []string
	for _, name := range t.Columns {
		if name == s.Dummy || s.tolerant(name) {
			continue
		}
		out = append(out, name)
	}
	return out
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
