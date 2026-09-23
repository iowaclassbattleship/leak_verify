package tabular

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"attribution/common/codec"
)

// Five carriers that sit outside the classic "change a value" family.
//
//   - Allocation withholds a keyed slice of rows per recipient. Nothing in the
//     data is touched; the copy is simply not quite complete.
//   - Ordering swaps adjacent rows. Content and row set are identical.
//   - Formatting changes how a number is written, not what it is. 1532.22 and
//     1532.220 are the same value.
//   - Noise perturbs a value the way a privacy pipeline already does, and the
//     sign of the perturbation carries the bit. Only honest where the owner
//     already adds noise of at least this size.
//   - Redaction replaces a personal value with a pseudonym. The redaction has
//     to emit some arbitrary token either way, so which token it emits is free
//     information and the recipient's mark rides in it at no cost to the data.

// Params are the tunables of each carrier. A zero field means "use the
// default", so an older caller keeps working.
type Params struct {
	CanaryRate  int `json:"canaryRate"`  // one canary per this many rows
	AllocRate   int `json:"allocRate"`   // one row in this many withheld
	LowBitGamma int `json:"lowBitGamma"` // one tolerant cell in this many carries a bit
	LowBitDepth int `json:"lowBitDepth"` // decimal places in from the last one, 0 = the last
	NoiseRate   int `json:"noiseRate"`   // one tolerant cell in this many carries a bit
	NoiseSpan   int `json:"noiseSpan"`   // perturbation size, in last declared digits
	OrderGamma  int `json:"orderGamma"`  // one adjacent pair in this many carries a bit
	FormatRate  int `json:"formatRate"`  // one decimal cell in this many carries a bit
}

func or(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func clamp(v, lo, hi int) int {
	return max(lo, min(hi, v))
}

// WithDefaults fills every unset field.
func (p Params) WithDefaults() Params {
	return Params{
		CanaryRate:  or(p.CanaryRate, 200),
		AllocRate:   or(p.AllocRate, 64),
		LowBitGamma: or(p.LowBitGamma, 2),
		LowBitDepth: clamp(p.LowBitDepth, 0, 8), // 0 is a real choice, so no default
		NoiseRate:   or(p.NoiseRate, 3),
		NoiseSpan:   or(p.NoiseSpan, 4),
		OrderGamma:  or(p.OrderGamma, 1),
		FormatRate:  or(p.FormatRate, 3),
	}
}

// withheld reports whether this recipient's copy leaves the row out.
func withheld(key codec.Key, pk string, id uint16, rate int) bool {
	return int(key.Sum("alloc", pk, strconv.Itoa(int(id)))[0])%rate == 0
}

// orderSlot says whether an adjacent pair carries a bit, which one, and the
// mask. The pair is addressed by the identifying value of its first row, so it
// survives the table being cut down.
func orderSlot(key codec.Key, pk string, gamma int) (selected bool, idx int, mask uint8) {
	s := key.Sum("order", pk)
	return int(s[0])%gamma == 0, int(s[1]) % codec.CodeLen, s[2] & 1
}

func formatSlot(key codec.Key, pk, field string, rate int) (selected bool, idx int, mask uint8) {
	s := key.Sum("format", pk, field)
	return int(s[0])%rate == 0, int(s[1]) % codec.CodeLen, s[2] & 1
}

func noiseSlot(key codec.Key, pk, field string, rate, span int) (selected bool, idx int, mask uint8, jitter int) {
	s := key.Sum("noise", pk, field)
	return int(s[0])%rate == 0, int(s[1]) % codec.CodeLen, s[2] & 1, 1 + int(s[3])%span
}

// padDecimal writes the same number with one more decimal place. The value is
// unchanged; only its representation differs.
func padDecimal(v string) (string, bool) {
	t := strings.TrimSpace(v)
	dot := strings.IndexByte(t, '.')
	if dot < 0 || t != v {
		return v, false
	}
	if _, err := strconv.ParseFloat(t, 64); err != nil {
		return v, false
	}
	for _, r := range t[dot+1:] {
		if r < '0' || r > '9' {
			return v, false
		}
	}
	return v + "0", true
}

// applyNoise moves a value by a few units of its last declared digit, in the
// direction the bit dictates.
func applyNoise(f Field, v string, bit uint8, jitter int) (string, bool) {
	if f.Timestamp || f.Decimals <= 0 {
		return v, false
	}
	x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || strings.IndexByte(v, '.') < 0 {
		return v, false
	}
	step := int64(jitter)
	if bit == 0 {
		step = -step
	}
	n := int64(math.Round(x*math.Pow10(f.Decimals))) + step
	return formatFixed(n, f.Decimals), true
}

// swapPairs reorders adjacent rows to carry the codeword. It returns how many
// pairs were swapped. Row content and the row set are untouched.
func swapPairs(key codec.Key, t *Table, sc Schema, code codec.Codeword, gamma int) int {
	swaps := 0
	for i := 0; i+1 < len(t.Rows); i += 2 {
		sel, idx, mask := orderSlot(key, sc.rowKey(t, t.Rows[i]), gamma)
		if !sel {
			continue
		}
		if code[idx]^mask == 1 {
			t.Rows[i], t.Rows[i+1] = t.Rows[i+1], t.Rows[i]
			swaps++
		}
	}
	return swaps
}

// redactValue is the pseudonym that stands in for a personal value. It is
// stable for the same input within a copy, and differs between copies.
func redactValue(key codec.Key, original string, id uint16) string {
	pad := binary.BigEndian.Uint16(key.Sum("redact", original))
	chk := key.Sum("redact-check", original, strconv.Itoa(int(id)))[0] % 100
	return fmt.Sprintf("PSN-%04X-%02d", id^pad, chk)
}

// redactable lists the columns the data owner marked for redaction that are
// present in t. The column used to locate marks is never among them, because
// replacing it would break every other carrier's addressing.
func (s Schema) redactable(t *Table) []string {
	var out []string
	for _, name := range s.Redact {
		if t.Col(name) >= 0 && name != s.Key && name != s.Dummy && !s.tolerant(name) {
			out = append(out, name)
		}
	}
	return out
}

// params are the tunables the issued copies were made with, so the detector
// looks in the same slots the marker used.
func (r *Registry) params() Params {
	for _, is := range r.Issued {
		return is.Techniques.Params.WithDefaults()
	}
	return Params{}.WithDefaults()
}

// shallower returns the field as seen at a decimal place further from the
// last one, so the same parity machinery can perturb a coarser digit. ok is
// false when the column has no digit that far in.
func shallower(f Field, depth int) (Field, bool) {
	f.Decimals -= depth
	return f, f.Decimals > 0
}

// decodeRedaction recovers the recipient from a pseudonym, given the value it
// replaced. ok is false unless the check value verifies.
func decodeRedaction(key codec.Key, original, v string) (uint16, bool) {
	var code, chk int
	if n, _ := fmt.Sscanf(v, "PSN-%04X-%02d", &code, &chk); n != 2 {
		return 0, false
	}
	id := uint16(code) ^ binary.BigEndian.Uint16(key.Sum("redact", original))
	return id, int(key.Sum("redact-check", original, strconv.Itoa(int(id)))[0]%100) == chk
}
