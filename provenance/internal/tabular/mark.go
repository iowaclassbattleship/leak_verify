package tabular

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"attribution/common/codec"
)

type Techniques struct {
	Canary   bool   `json:"canary"`
	LowBit   bool   `json:"lowBit"`
	Dummy    bool   `json:"dummy"`
	Allocate bool   `json:"allocate"`
	Order    bool   `json:"order"`
	Format   bool   `json:"format"`
	Noise    bool   `json:"noise"`
	Redact   bool   `json:"redact"`
	Params   Params `json:"params"`
}

// Canary is one synthetic row planted in a recipient's copy.
type Canary struct {
	Row   []string          `json:"-"`
	Ident map[string]string `json:"ident"` // identifying values, for the log
}

type Issuance struct {
	MarkID     uint16     `json:"-"`
	Mark       string     `json:"markId"`
	Recipient  string     `json:"recipient"`
	Org        string     `json:"org"`  // the purpose the data was issued for
	Unit       string     `json:"unit"` // where the recipient sits in the hierarchy
	Techniques Techniques `json:"techniques"`
	IssuedAt   time.Time  `json:"issuedAt"`
	Rows       int        `json:"rows"`
	Canaries   []Canary   `json:"canaries"`
	MarkedCell int        `json:"markedCells"`
	Changed    int        `json:"changedCells"`
	Withheld   int        `json:"withheldRows"`
	Swapped    int        `json:"swappedPairs"`
	Reformat   int        `json:"reformattedCells"`
	Noised     int        `json:"noisedCells"`
	Redacted   int        `json:"redactedCells"`
	FileName   string     `json:"fileName"`
}

// lowBitSlot decides, from the secret key and the row's identifying value only,
// whether a cell is marked, which codeword bit it carries and its XOR mask.
func lowBitSlot(key codec.Key, pk, field string, gamma int) (selected bool, idx int, mask uint8) {
	s := key.Sum("lowbit", pk, field)
	return int(s[0])%gamma == 0, int(s[1]) % codec.CodeLen, s[2] & 1
}

func dummyValue(key codec.Key, pk string, id uint16) string {
	pad := binary.BigEndian.Uint16(key.Sum("dummy", pk))
	chk := key.Sum("dummy-check", pk, strconv.Itoa(int(id)))[0] % 100
	return fmt.Sprintf("BR-%04X-%02d", id^pad, chk)
}

// decodeDummy returns the mark ID hidden in a dummy column value, if its check digits verify.
func decodeDummy(key codec.Key, pk, v string) (uint16, bool) {
	var code, chk int
	if n, _ := fmt.Sscanf(v, "BR-%04X-%02d", &code, &chk); n != 2 {
		return 0, false
	}
	id := uint16(code) ^ binary.BigEndian.Uint16(key.Sum("dummy", pk))
	return id, int(key.Sum("dummy-check", pk, strconv.Itoa(int(id)))[0]%100) == chk
}

// readParity extracts the low-order bit of a tolerant value. ok is false when
// the value no longer has the marked precision, for example after rounding.
func readParity(f Field, v string) (bit uint8, ok bool) {
	if f.Timestamp {
		m := reTimestamp.FindStringSubmatch(v)
		if m == nil || len(m[1]) < f.Decimals {
			return 0, false
		}
		n, err := strconv.Atoi(m[1][:f.Decimals])
		if err != nil {
			return 0, false
		}
		return uint8(n & 1), true
	}
	dot := strings.IndexByte(v, '.')
	if dot < 0 || len(v)-dot-1 < f.Decimals {
		return 0, false
	}
	x, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	n := int64(math.Round(math.Abs(x) * math.Pow10(f.Decimals)))
	return uint8(n & 1), true
}

func writeParity(f Field, v string, bit uint8) (string, bool) {
	cur, ok := readParity(f, v)
	if !ok || cur == bit {
		return v, false
	}
	if f.Timestamp {
		m := reTimestamp.FindStringSubmatchIndex(v)
		frac := v[m[2]:m[3]]
		n, _ := strconv.Atoi(frac[:f.Decimals])
		return v[:m[2]] + fmt.Sprintf("%0*d", f.Decimals, n^1) + frac[f.Decimals:] + v[m[3]:], true
	}
	// Flip the parity of the digit at f.Decimals while keeping every digit
	// below it, so marking a coarser place is still a nudge and not a rounding.
	dot := strings.IndexByte(v, '.')
	x, err := strconv.ParseFloat(v, 64)
	if dot < 0 || err != nil {
		return v, false
	}
	shown := len(v) - dot - 1
	step := int64(math.Pow10(shown - f.Decimals))
	full := int64(math.Round(math.Abs(x) * math.Pow10(shown)))
	for _, delta := range []int64{step, -step, 2 * step} {
		out := full + delta
		if out < 0 {
			continue
		}
		cand := formatFixed(out, shown)
		if x < 0 {
			cand = "-" + cand
		}
		if got, ok := readParity(f, cand); ok && got == bit {
			return cand, true
		}
	}
	return v, false
}

// MarkCopy produces the recipient-specific copy of master. It is deterministic
// in (key, master, schema, id, techniques), so the detector can regenerate any
// issued copy for exact fingerprint comparison.
func MarkCopy(key codec.Key, master *Table, sc Schema, id uint16, tech Techniques) (*Table, *Issuance) {
	t := master.Clone()
	iss := &Issuance{MarkID: id, Mark: codec.FormatID(id), Techniques: tech}
	code := key.Encode(id)
	par := tech.Params.WithDefaults()

	if tech.Allocate {
		kept := t.Rows[:0]
		for _, row := range t.Rows {
			if withheld(key, sc.rowKey(t, row), id, par.AllocRate) {
				iss.Withheld++
				continue
			}
			kept = append(kept, row)
		}
		t.Rows = kept
	}

	if tech.Redact {
		for _, name := range sc.redactable(t) {
			c := t.Col(name)
			for _, row := range t.Rows {
				row[c] = redactValue(key, row[c], id)
				iss.Redacted++
			}
		}
	}

	if tech.Noise {
		for _, row := range t.Rows {
			pk := sc.rowKey(t, row)
			for _, f := range sc.Tolerant {
				c := t.Col(f.Name)
				if c < 0 {
					continue
				}
				sel, idx, mask, jitter := noiseSlot(key, pk, f.Name, par.NoiseRate, par.NoiseSpan)
				if !sel {
					continue
				}
				if nv, ok := applyNoise(f, row[c], code[idx]^mask, jitter); ok {
					row[c] = nv
					iss.Noised++
				}
			}
		}
	}

	if tech.LowBit {
		for _, row := range t.Rows {
			pk := sc.rowKey(t, row)
			for _, f := range sc.Tolerant {
				c := t.Col(f.Name)
				if c < 0 {
					continue
				}
				df, ok := shallower(f, par.LowBitDepth)
				if !ok {
					continue
				}
				sel, idx, mask := lowBitSlot(key, pk, f.Name, par.LowBitGamma)
				if !sel {
					continue
				}
				iss.MarkedCell++
				if nv, changed := writeParity(df, row[c], code[idx]^mask); changed {
					row[c] = nv
					iss.Changed++
				}
			}
		}
	}

	if tech.Format {
		for _, row := range t.Rows {
			pk := sc.rowKey(t, row)
			for c, name := range t.Columns {
				if name == sc.Key {
					continue
				}
				sel, idx, mask := formatSlot(key, pk, name, par.FormatRate)
				if !sel || code[idx]^mask == 0 {
					continue
				}
				if nv, ok := padDecimal(row[c]); ok {
					row[c] = nv
					iss.Reformat++
				}
			}
		}
	}

	if tech.Canary && len(master.Rows) > 0 {
		// Seeded by the dataset as well as the recipient, so two tables issued
		// under the same mark never share a canary.
		seed := binary.BigEndian.Uint64(key.Sum("canary-seed", iss.Mark, master.fingerprint()))
		rng := rand.New(rand.NewPCG(seed, ^seed))
		taken := map[string]bool{}
		for _, row := range t.Rows {
			taken[sc.rowKey(t, row)] = true
		}
		n := max(1, len(master.Rows)/par.CanaryRate)
		for i := 0; i < n; i++ {
			row, ok := sc.synthesise(t, master, rng, taken)
			if !ok {
				break
			}
			taken[sc.rowKey(t, row)] = true
			ident := map[string]string{}
			for _, name := range sc.identifying(t) {
				ident[name] = row[t.Col(name)]
			}
			iss.Canaries = append(iss.Canaries, Canary{Row: row, Ident: ident})
			pos := rng.IntN(len(t.Rows) + 1)
			t.Rows = append(t.Rows, nil)
			copy(t.Rows[pos+1:], t.Rows[pos:])
			t.Rows[pos] = row
		}
	}

	if tech.Dummy && sc.Dummy != "" {
		t.Columns = append(t.Columns, sc.Dummy)
		for i, row := range t.Rows {
			t.Rows[i] = append(row, dummyValue(key, sc.rowKey(t, row), id))
		}
	}
	if tech.Order {
		iss.Swapped = swapPairs(key, t, sc, code, par.OrderGamma)
	}
	iss.Rows = len(t.Rows)
	return t, iss
}

// identifying lists the columns a canary row must not share with a real one.
func (s Schema) identifying(t *Table) []string {
	var out []string
	if s.Key != "" && t.Col(s.Key) >= 0 {
		out = append(out, s.Key)
	}
	for _, m := range s.Match {
		if t.Col(m) >= 0 {
			out = append(out, m)
		}
	}
	return out
}

// synthesise builds a canary row: a real row's values recombined, with fresh
// identifying values, so it is plausible in every column but belongs to nobody.
// Identifying values are drawn from inside the range and format the column
// already uses, and personal columns are recombined from other rows, so no
// real person's details appear in a synthetic row.
//
// Keys already in the source count as used even when this copy withholds
// their row: another recipient's copy has that row, and a canary sharing its
// key would be found there.
func (s Schema) synthesise(t, master *Table, rng *rand.Rand, taken map[string]bool) ([]string, bool) {
	ident := s.identifying(t)
	for attempt := 0; attempt < 40; attempt++ {
		row := append([]string(nil), t.Rows[rng.IntN(len(t.Rows))]...)
		for _, name := range ident {
			c := t.Col(name)
			row[c] = freshValue(t, master, c, rng, taken)
		}
		for _, name := range s.Redact {
			if c := t.Col(name); c >= 0 && !slicesContains(ident, name) {
				row[c] = recombine(t, c, rng)
			}
		}
		for _, f := range s.Tolerant {
			c := t.Col(f.Name)
			if c < 0 || f.Timestamp {
				continue
			}
			if x, err := strconv.ParseFloat(row[c], 64); err == nil {
				p := math.Pow10(f.Decimals)
				row[c] = formatFixed(int64(math.Round(x*p))+int64(rng.IntN(19)-9), f.Decimals)
			}
		}
		if fresh(t, row, ident, taken) && fresh(master, row, ident, taken) {
			return row, true
		}
	}
	return nil, false
}

func slicesContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var reLastDigits = regexp.MustCompile(`\d+(?:\D*$)`)

// freshValue draws an unused value for an identifying column. When the values
// carry a number (1042, AC-104233, P-0042-X), the number is drawn from the
// range the column already covers, with the same width and padding, and only
// runs past the top when the range is full, the way a new record would. A
// value without digits is spliced from two others.
func freshValue(t, master *Table, c int, rng *rand.Rand, taken map[string]bool) string {
	donor := t.Rows[rng.IntN(len(t.Rows))][c]
	loc := reLastDigits.FindStringIndex(donor)
	if loc == nil {
		other := t.Rows[rng.IntN(len(t.Rows))][c]
		if i, j := strings.IndexAny(donor, "@/"), strings.IndexAny(other, "@/"); i > 0 && j > 0 {
			donor = donor[:i] + other[j:]
		}
		return donor
	}
	end := loc[0]
	for end < len(donor) && donor[end] >= '0' && donor[end] <= '9' {
		end++
	}
	prefix, digits, suffix := donor[:loc[0]], donor[loc[0]:end], donor[end:]
	width := len(digits)

	used := map[int]bool{}
	lo, hi, padded := math.MaxInt, math.MinInt, false
	mc := master.Col(t.Columns[c])
	values := make([]string, 0, len(t.Rows)+len(master.Rows))
	for _, row := range t.Rows {
		values = append(values, row[c])
	}
	if mc >= 0 {
		for _, row := range master.Rows {
			values = append(values, row[mc])
		}
	}
	for _, v := range values {
		if !strings.HasPrefix(v, prefix) || !strings.HasSuffix(v, suffix) || len(v) < len(prefix)+len(suffix)+1 {
			continue
		}
		mid := v[len(prefix) : len(v)-len(suffix)]
		n, err := strconv.Atoi(mid)
		if err != nil || n < 0 {
			continue
		}
		used[n] = true
		lo, hi = min(lo, n), max(hi, n)
		padded = padded || (len(mid) > 1 && mid[0] == '0')
	}
	format := func(n int) string {
		if padded {
			return fmt.Sprintf("%s%0*d%s", prefix, width, n, suffix)
		}
		return fmt.Sprintf("%s%d%s", prefix, n, suffix)
	}
	if lo > hi {
		return donor
	}
	for try := 0; try < 60; try++ {
		n := lo + rng.IntN(hi-lo+1)
		if v := format(n); !used[n] && !taken[v] {
			return v
		}
	}
	// The range is full: continue past the top, with random spacing so the
	// canaries are not one consecutive block and differ between datasets.
	spread := max(8, (hi-lo+1)/50)
	for n := hi + 1 + rng.IntN(spread); ; n += 1 + rng.IntN(3) {
		if v := format(n); !used[n] && !taken[v] {
			return v
		}
	}
}

// DenseKey reports whether the identifying column is a number range with no
// gaps, so a canary key has to lie past its top and shows in a sorted copy.
func (s Schema) DenseKey(t *Table) bool {
	c := t.Col(s.Key)
	if c < 0 || len(t.Rows) < 2 {
		return false
	}
	lo, hi := math.MaxInt, math.MinInt
	for _, row := range t.Rows {
		n, err := strconv.Atoi(row[c])
		if err != nil {
			return false
		}
		lo, hi = min(lo, n), max(hi, n)
	}
	return hi-lo+1 == len(t.Rows)
}

// recombine builds a plausible personal value from parts of different rows:
// a first name from one and a surname from another, or an address's local
// part from one and its domain from another. An email address that happens
// to come out as a real one is tried again, since it would reach a person.
func recombine(t *Table, c int, rng *rand.Rand) string {
	pick := func() string { return t.Rows[rng.IntN(len(t.Rows))][c] }
	existing := map[string]bool{}
	for _, row := range t.Rows {
		existing[row[c]] = true
	}
	var v string
	for try := 0; try < 20; try++ {
		a, b := pick(), pick()
		v = a
		if i, j := strings.IndexByte(a, '@'), strings.IndexByte(b, '@'); i > 0 && j > 0 {
			la, lb := a[:i], b[:j]
			if k, m := strings.IndexAny(la, "._"), strings.IndexAny(lb, "._"); k > 0 && m > 0 {
				la = la[:k] + lb[m:]
			}
			v = la + b[j:]
			if !existing[v] {
				return v
			}
			continue
		}
		if i, j := strings.IndexByte(a, ' '), strings.LastIndexByte(b, ' '); i > 0 && j > 0 {
			v = a[:i] + b[j:]
		}
		// A name shared with a real person is normal; only an exact address is not.
		return v
	}
	if i := strings.IndexByte(v, '@'); i > 0 {
		return fmt.Sprintf("%s%d%s", v[:i], 100+rng.IntN(900), v[i:])
	}
	return v
}

// fresh reports whether a synthesised row collides with an existing one.
func fresh(t *Table, row []string, ident []string, taken map[string]bool) bool {
	for _, name := range ident {
		c := t.Col(name)
		for _, other := range t.Rows {
			if other[c] == row[c] {
				return false
			}
		}
	}
	return true
}
