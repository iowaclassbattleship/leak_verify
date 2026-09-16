package tabular

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"custodial/internal/codec"
)

type Techniques struct {
	Canary bool `json:"canary"`
	LowBit bool `json:"lowBit"`
	Dummy  bool `json:"dummy"`
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
	Org        string     `json:"org"`
	Techniques Techniques `json:"techniques"`
	IssuedAt   time.Time  `json:"issuedAt"`
	Rows       int        `json:"rows"`
	Canaries   []Canary   `json:"canaries"`
	MarkedCell int        `json:"markedCells"`
	Changed    int        `json:"changedCells"`
	FileName   string     `json:"fileName"`
}

// lowBitGamma: one in gamma tolerant cells carries a codeword bit.
const lowBitGamma = 2

// lowBitSlot decides, from the secret key and the row's identifying value only,
// whether a cell is marked, which codeword bit it carries and its XOR mask.
func lowBitSlot(key codec.Key, pk, field string) (selected bool, idx int, mask uint8) {
	s := key.Sum("lowbit", pk, field)
	return s[0]%lowBitGamma == 0, int(s[1]) % codec.CodeLen, s[2] & 1
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
	x, _ := strconv.ParseFloat(v, 64)
	n := int64(math.Round(x * math.Pow10(f.Decimals)))
	if n < 0 {
		n = -((-n) ^ 1)
	} else {
		n ^= 1
	}
	return formatFixed(n, f.Decimals), true
}

// MarkCopy produces the recipient-specific copy of master. It is deterministic
// in (key, master, schema, id, techniques), so the detector can regenerate any
// issued copy for exact fingerprint comparison.
func MarkCopy(key codec.Key, master *Table, sc Schema, id uint16, tech Techniques) (*Table, *Issuance) {
	t := master.Clone()
	iss := &Issuance{MarkID: id, Mark: codec.FormatID(id), Techniques: tech}
	code := key.Encode(id)

	if tech.LowBit {
		for _, row := range t.Rows {
			pk := sc.rowKey(t, row)
			for _, f := range sc.Tolerant {
				c := t.Col(f.Name)
				if c < 0 {
					continue
				}
				sel, idx, mask := lowBitSlot(key, pk, f.Name)
				if !sel {
					continue
				}
				iss.MarkedCell++
				if nv, changed := writeParity(f, row[c], code[idx]^mask); changed {
					row[c] = nv
					iss.Changed++
				}
			}
		}
	}

	if tech.Canary && len(master.Rows) > 0 {
		seed := binary.BigEndian.Uint64(key.Sum("canary-seed", iss.Mark))
		rng := rand.New(rand.NewPCG(seed, ^seed))
		taken := map[string]bool{}
		for _, row := range t.Rows {
			taken[sc.rowKey(t, row)] = true
		}
		n := max(5, len(master.Rows)/200)
		for i := 0; i < n; i++ {
			row, ok := sc.synthesise(t, rng, taken)
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
func (s Schema) synthesise(t *Table, rng *rand.Rand, taken map[string]bool) ([]string, bool) {
	ident := s.identifying(t)
	for attempt := 0; attempt < 40; attempt++ {
		row := append([]string(nil), t.Rows[rng.IntN(len(t.Rows))]...)
		for _, name := range ident {
			c := t.Col(name)
			donor := t.Rows[rng.IntN(len(t.Rows))][c]
			other := t.Rows[rng.IntN(len(t.Rows))][c]
			// Values with structure (an address, a code) keep their shape:
			// splice a second value in at the separator, then reroll the digits.
			if i, j := strings.IndexAny(donor, "@/"), strings.IndexAny(other, "@/"); i > 0 && j > 0 {
				donor = donor[:i] + other[j:]
			}
			row[c] = mutateValue(donor, rng.IntN)
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
		if fresh(t, row, ident, taken) {
			return row, true
		}
	}
	return nil, false
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
