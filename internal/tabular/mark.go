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

type Canary struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	City      string `json:"city"`
	Balance   string `json:"balance"`
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
}

const (
	DummyColumn = "branch_ref"
	// lowBitGamma: one in gamma tolerant cells carries a codeword bit.
	lowBitGamma = 2
)

// lowBitSlot decides, from the secret key and the row's primary key only,
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

// decodeDummy returns the mark ID hidden in a branch_ref value, if its check digits verify.
func decodeDummy(key codec.Key, pk, v string) (uint16, bool) {
	var code, chk int
	if n, _ := fmt.Sscanf(v, "BR-%04X-%02d", &code, &chk); n != 2 {
		return 0, false
	}
	id := uint16(code) ^ binary.BigEndian.Uint16(key.Sum("dummy", pk))
	return id, int(key.Sum("dummy-check", pk, strconv.Itoa(int(id)))[0]%100) == chk
}

// readParity extracts the low-order bit of a tolerant value. ok is false when
// the value no longer has the marked precision (e.g. it was rounded away).
func readParity(f Field, v string) (bit uint8, ok bool) {
	if f.Timestamp {
		if len(v) != len(tsLayout) || v[19] != '.' {
			return 0, false
		}
		ms, err := strconv.Atoi(v[20:])
		if err != nil {
			return 0, false
		}
		return uint8(ms & 1), true
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
		ms, _ := strconv.Atoi(v[20:])
		return fmt.Sprintf("%s%03d", v[:20], ms^1), true
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

// MarkCopy produces the recipient-specific copy of master. It is
// deterministic in (key, master, id, techniques), so the detector can
// regenerate any issued copy for exact fingerprint comparison.
func MarkCopy(key codec.Key, master *Table, id uint16, tech Techniques) (*Table, *Issuance) {
	t := master.Clone()
	iss := &Issuance{MarkID: id, Mark: codec.FormatID(id), Techniques: tech}
	pkCol := t.Col("account_id")
	code := key.Encode(id)

	if tech.LowBit {
		for _, row := range t.Rows {
			for _, f := range Tolerant {
				c := t.Col(f.Name)
				if c < 0 {
					continue
				}
				sel, idx, mask := lowBitSlot(key, row[pkCol], f.Name)
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

	if tech.Canary {
		seed := binary.BigEndian.Uint64(key.Sum("canary-seed", iss.Mark))
		rng := rand.New(rand.NewPCG(seed, ^seed))
		usedIDs, usedEmails := map[string]bool{}, map[string]bool{}
		for _, r := range master.Rows {
			usedIDs[r[pkCol]] = true
			usedEmails[r[2]] = true
		}
		n := max(5, len(master.Rows)/200)
		for i := 0; i < n; i++ {
			row := genRow(rng, usedIDs, usedEmails)
			iss.Canaries = append(iss.Canaries, Canary{AccountID: row[0], Name: row[1], Email: row[2], City: row[3], Balance: row[5]})
			pos := rng.IntN(len(t.Rows) + 1)
			t.Rows = append(t.Rows, nil)
			copy(t.Rows[pos+1:], t.Rows[pos:])
			t.Rows[pos] = row
		}
	}

	if tech.Dummy {
		at := t.Col("city") + 1
		t.Columns = append(t.Columns[:at], append([]string{DummyColumn}, t.Columns[at:]...)...)
		for i, row := range t.Rows {
			v := dummyValue(key, row[pkCol], id)
			t.Rows[i] = append(row[:at], append([]string{v}, row[at:]...)...)
		}
	}
	iss.Rows = len(t.Rows)
	return t, iss
}
