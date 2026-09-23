package tabular

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"attribution/common/codec"
)

// customers is the QA's table: a numeric key, names, an email, a city, a
// balance and a signup date.
func customers(n, firstID int, seed uint64) *Table {
	rng := rand.New(rand.NewPCG(seed, seed))
	t := &Table{Columns: []string{"customer_id", "first_name", "last_name", "email", "city", "balance", "signup_date"}}
	for i := 0; i < n; i++ {
		first, last := firstNames[rng.IntN(len(firstNames))], lastNames[rng.IntN(len(lastNames))]
		t.Rows = append(t.Rows, []string{
			strconv.Itoa(firstID + i), first, last,
			fmt.Sprintf("%s.%s%d@example.ch", strings.ToLower(asciiFold(first)), strings.ToLower(asciiFold(last)), i),
			cities[rng.IntN(len(cities))].Name,
			fmt.Sprintf("%.2f", rng.Float64()*9000),
			fmt.Sprintf("2024-%02d-%02d", 1+rng.IntN(12), 1+rng.IntN(28)),
		})
	}
	return t
}

func TestRedactionRolesDetected(t *testing.T) {
	tab := customers(500, 1000, 1)
	sc, cols := DetectSchema(tab)
	if got := strings.Join(sc.Redact, ","); got != "first_name,last_name,email" {
		t.Errorf("redact %q, want first_name,last_name,email", got)
	}
	if sc.Key != "customer_id" {
		t.Errorf("key %q", sc.Key)
	}
	for _, c := range cols {
		if c.Name == "signup_date" && c.Kind != "date" {
			t.Errorf("signup_date profiled as %s", c.Kind)
		}
	}

	// Marked with redaction on, dates and numbers are untouched, and exactly
	// the three personal columns are replaced.
	marked, iss := MarkCopy(codec.NewKey(), tab, sc, 0x1234, Techniques{Redact: true})
	for c, name := range tab.Columns {
		changed := 0
		for i := range tab.Rows {
			if marked.Rows[i][c] != tab.Rows[i][c] {
				changed++
			}
		}
		personal := name == "first_name" || name == "last_name" || name == "email"
		if personal && changed != len(tab.Rows) || !personal && changed != 0 {
			t.Errorf("%s: %d of %d values changed", name, changed, len(tab.Rows))
		}
	}
	if iss.Redacted != 3*len(tab.Rows) {
		t.Errorf("redacted %d", iss.Redacted)
	}
}

// Company and address columns: an address is personal, a company is not.
func TestPersonalHeaders(t *testing.T) {
	for name, want := range map[string]bool{
		"first_name": true, "Nachname": true, "E-Mail": true, "address": true, "Telefon": true,
		"company": false, "company_name": false, "city": false, "signup_date": false, "balance": false, "product_name": false,
	} {
		if got := personalHeader(name); got != want {
			t.Errorf("%s: personal %v, want %v", name, got, want)
		}
	}
}

// Canary keys sit inside the range the column covers, and two unrelated
// tables marked with the same mark ID do not share canaries.
func TestCanaryKeysPlausible(t *testing.T) {
	key := codec.NewKey()
	sparse := customers(300, 0, 2)
	// Leave gaps, as real key columns have.
	var kept [][]string
	for i, r := range sparse.Rows {
		if i%3 != 1 {
			kept = append(kept, r)
		}
	}
	sparse.Rows = kept
	canaryKeys := func(tab *Table) []int {
		sc, _ := DetectSchema(tab)
		_, iss := MarkCopy(key, tab, sc, 0x2C41, Techniques{Canary: true, Params: Params{CanaryRate: 20}})
		var out []int
		for _, c := range iss.Canaries {
			n, err := strconv.Atoi(c.Ident["customer_id"])
			if err != nil {
				t.Fatalf("canary key %q is not a number", c.Ident["customer_id"])
			}
			out = append(out, n)
		}
		return out
	}
	for _, n := range canaryKeys(sparse) {
		if n < 0 || n > 299 {
			t.Errorf("canary key %d outside 0-299, although the range has gaps", n)
		}
	}
	dense := customers(500, 1000, 3)
	a := canaryKeys(dense)
	for _, n := range a {
		if n < 1000 || n > 1499+8+3*len(a) {
			t.Errorf("canary key %d far outside 1000-1499", n)
		}
	}
	other := customers(500, 1000, 4) // another dataset, same key range
	b := canaryKeys(other)
	if fmt.Sprint(a) == fmt.Sprint(b) {
		t.Errorf("two datasets got the same canary keys %v", a)
	}
}

// A canary row never carries a real person's name or email.
func TestCanaryPersonalValuesRecombined(t *testing.T) {
	tab := customers(400, 1000, 5)
	sc, _ := DetectSchema(tab)
	_, iss := MarkCopy(codec.NewKey(), tab, sc, 0x0BEE, Techniques{Canary: true, Params: Params{CanaryRate: 20}})
	real := map[string]bool{}
	for _, r := range tab.Rows {
		real[r[3]] = true
	}
	for _, c := range iss.Canaries {
		if real[c.Row[3]] {
			t.Errorf("canary reuses a real email %s", c.Row[3])
		}
	}
}

// With allocation on, a copy withholds some real rows. A canary must not take
// the key of one of them: another recipient's copy holds that row, and the
// canary lookup would then name this recipient for the other's copy.
func TestCanaryKeysAvoidWithheldRows(t *testing.T) {
	tab := customers(600, 2000, 6)
	sc, _ := DetectSchema(tab)
	real := map[string]bool{}
	for _, r := range tab.Rows {
		real[r[0]] = true
	}
	key := codec.NewKey()
	for id := uint16(1); id < 40; id++ {
		_, iss := MarkCopy(key, tab, sc, id*997, Techniques{Canary: true, Allocate: true, Params: Params{CanaryRate: 50, AllocRate: 8}})
		for _, c := range iss.Canaries {
			if real[c.Ident["customer_id"]] {
				t.Fatalf("mark %d: canary reuses the real key %s", id, c.Ident["customer_id"])
			}
		}
	}
}
