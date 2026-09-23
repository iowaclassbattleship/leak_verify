// Package codec turns a 16-bit mark ID into a 32-bit error-correcting
// codeword, collects noisy per-bit votes recovered from a leaked artifact, and
// decides whether those votes attribute the artifact to an issued recipient.
//
// Code construction: each of the four nibbles of the ID is encoded with an
// extended Hamming(8,4) code (minimum distance 4), then the 32 bits are XORed
// with a key-derived whitening mask so codewords look random and are balanced.
// Decoding is soft-decision per nibble (maximum correlation over all 16
// nibbles), which tolerates erasures as well as flips.
package codec

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const CodeLen = 32

// Key is the issuer's marking secret. It never leaves the environment.
type Key []byte

func NewKey() Key {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

// Sum is a domain-separated HMAC over the given parts.
func (k Key) Sum(parts ...string) []byte {
	m := hmac.New(sha256.New, k)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return m.Sum(nil)
}

func (k Key) Uint16(parts ...string) uint16 {
	return binary.BigEndian.Uint16(k.Sum(parts...))
}

type Codeword [CodeLen]uint8

func (c Codeword) String() string {
	var b strings.Builder
	for i, v := range c {
		if i > 0 && i%8 == 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('0' + v)
	}
	return b.String()
}

func hamming84(n uint8) [8]uint8 {
	d1, d2, d3, d4 := n>>3&1, n>>2&1, n>>1&1, n&1
	p1 := d1 ^ d2 ^ d4
	p2 := d1 ^ d3 ^ d4
	p3 := d2 ^ d3 ^ d4
	return [8]uint8{p1, p2, d1, p3, d2, d3, d4, p1 ^ p2 ^ d1 ^ p3 ^ d2 ^ d3 ^ d4}
}

func (k Key) whitening() Codeword {
	s := k.Sum("codec-whitening")
	var w Codeword
	for i := range w {
		w[i] = s[i/8] >> (i % 8) & 1
	}
	return w
}

// Encode returns the whitened codeword carried by every marking layer.
func (k Key) Encode(id uint16) Codeword {
	w := k.whitening()
	var c Codeword
	for n := 0; n < 4; n++ {
		h := hamming84(uint8(id >> (12 - 4*n) & 0xF))
		for j := 0; j < 8; j++ {
			c[n*8+j] = h[j] ^ w[n*8+j]
		}
	}
	return c
}

func Distance(a, b Codeword) int {
	d := 0
	for i := range a {
		if a[i] != b[i] {
			d++
		}
	}
	return d
}

// AllocateID picks a random unused mark ID whose codeword is far from every
// existing one, so a few bit errors can never turn one recipient into another.
func (k Key) AllocateID(existing []uint16) uint16 {
	for attempt := 0; ; attempt++ {
		var b [2]byte
		rand.Read(b[:])
		id := binary.BigEndian.Uint16(b[:])
		if id == 0 {
			continue
		}
		minDist := 12
		if attempt > 2000 {
			minDist = 8
		}
		ok := true
		c := k.Encode(id)
		for _, e := range existing {
			if e == id || Distance(c, k.Encode(e)) < minDist {
				ok = false
				break
			}
		}
		if ok {
			return id
		}
	}
}

func FormatID(id uint16) string { return fmt.Sprintf("MK-%04X", id) }

// Votes accumulates evidence for each codeword bit.
type Votes struct {
	One, Zero [CodeLen]float64
}

func (v *Votes) Add(idx int, bit uint8, weight float64) {
	if bit == 1 {
		v.One[idx%CodeLen] += weight
	} else {
		v.Zero[idx%CodeLen] += weight
	}
}

func (v *Votes) Merge(o Votes) {
	for i := range v.One {
		v.One[i] += o.One[i]
		v.Zero[i] += o.Zero[i]
	}
}

func (v Votes) Total() float64 {
	t := 0.0
	for i := range v.One {
		t += v.One[i] + v.Zero[i]
	}
	return t
}

// Candidate is an issued mark that a decision may attribute to.
type Candidate struct {
	ID    uint16
	Label string
}

type Score struct {
	MarkID    string  `json:"markId"`
	Recipient string  `json:"recipient"`
	Agreement float64 `json:"agreement"`
	Errors    int     `json:"errors"`
}

type Decision struct {
	Status       string  `json:"status"` // attributed | inconclusive | absent
	MarkID       string  `json:"markId,omitempty"`
	Recipient    string  `json:"recipient,omitempty"`
	DecodedID    string  `json:"decodedId,omitempty"`
	DecodedInLog bool    `json:"decodedInLog"`
	BitsObserved int     `json:"bitsObserved"`
	BitErrors    int     `json:"bitErrors"`
	Votes        float64 `json:"votes"`
	FalseProb    float64 `json:"falseProb"`
	Ranking      []Score `json:"ranking,omitempty"`
}

// MaxFalseProb is the attribution threshold: the probability that an
// unrelated artifact matches some issued codeword this well by chance.
const MaxFalseProb = 1e-3

// ChanceLevel: evidence an unrelated artifact would reach this often is
// reported as "absent" rather than "inconclusive". Between MaxFalseProb and
// ChanceLevel a tag is possible but not attributable.
const ChanceLevel = 0.05

// Settle downgrades an inconclusive decision whose evidence is chance-level.
func (d *Decision) Settle() {
	if d.Status == "inconclusive" && d.FalseProb >= ChanceLevel {
		d.Status = "absent"
	}
}

// Decode majority-votes each bit, ECC-decodes the ID, and ranks the issued
// candidates by agreement with the observed bits. Attribution requires a
// unique best candidate whose chance-match probability is below MaxFalseProb.
func (k Key) Decode(v Votes, cands []Candidate) Decision {
	d := Decision{Votes: v.Total(), FalseProb: 1}
	if d.Votes == 0 {
		d.Status = "absent"
		return d
	}
	w := k.whitening()
	var soft [CodeLen]float64
	var observed [CodeLen]bool
	var majority Codeword
	for i := range soft {
		s := v.One[i] - v.Zero[i]
		if s != 0 {
			observed[i] = true
			d.BitsObserved++
			if s > 0 {
				majority[i] = 1
			}
		}
		if w[i] == 1 {
			s = -s
		}
		soft[i] = s
	}
	var id uint16
	for n := 0; n < 4; n++ {
		best, bestScore := 0, math.Inf(-1)
		for cand := 0; cand < 16; cand++ {
			h := hamming84(uint8(cand))
			sc := 0.0
			for j := 0; j < 8; j++ {
				if h[j] == 1 {
					sc += soft[n*8+j]
				} else {
					sc -= soft[n*8+j]
				}
			}
			if sc > bestScore {
				best, bestScore = cand, sc
			}
		}
		id = id<<4 | uint16(best)
	}
	d.DecodedID = FormatID(id)

	errorsAgainst := func(c Codeword) int {
		e := 0
		for i := range c {
			if observed[i] && c[i] != majority[i] {
				e++
			}
		}
		return e
	}
	for _, c := range cands {
		e := errorsAgainst(k.Encode(c.ID))
		agree := 0.0
		if d.BitsObserved > 0 {
			agree = float64(d.BitsObserved-e) / float64(d.BitsObserved)
		}
		d.Ranking = append(d.Ranking, Score{MarkID: FormatID(c.ID), Recipient: c.Label, Agreement: agree, Errors: e})
		if c.ID == id {
			d.DecodedInLog = true
		}
	}
	sort.SliceStable(d.Ranking, func(i, j int) bool { return d.Ranking[i].Errors < d.Ranking[j].Errors })

	d.Status = "inconclusive"
	if len(d.Ranking) == 0 {
		d.BitErrors = errorsAgainst(k.Encode(id))
		d.FalseProb = 1
		d.Settle()
		return d
	}
	best := d.Ranking[0]
	d.BitErrors = best.Errors
	d.FalseProb = math.Min(1, binomTail(d.BitsObserved, best.Errors)*float64(len(cands)))
	unique := len(d.Ranking) == 1 || d.Ranking[1].Errors > best.Errors
	if unique && d.FalseProb <= MaxFalseProb {
		d.Status = "attributed"
		d.MarkID = best.MarkID
		d.Recipient = best.Recipient
	}
	if len(d.Ranking) > 5 {
		d.Ranking = d.Ranking[:5]
	}
	d.Settle()
	return d
}

// binomTail is P(a uniformly random k-bit word is within e bits of a fixed word).
func binomTail(k, e int) float64 {
	if k == 0 {
		return 1
	}
	sum, c := 0.0, 1.0
	for j := 0; j <= e && j <= k; j++ {
		if j > 0 {
			c = c * float64(k-j+1) / float64(j)
		}
		sum += c
	}
	return sum / math.Pow(2, float64(k))
}

// FormatProb renders a chance-match probability for humans.
func FormatProb(p float64) string {
	switch {
	case p >= 0.5:
		return "No better than chance"
	case p >= 0.01:
		return fmt.Sprintf("%.0f%% chance of a coincidental match", p*100)
	case p <= 1e-15:
		return "False match odds below 1 in 10^15"
	default:
		return fmt.Sprintf("False match odds 1 in %s", humanInt(1/p))
	}
}

func humanInt(x float64) string {
	if x >= 1e6 {
		return fmt.Sprintf("10^%.0f", math.Floor(math.Log10(x)))
	}
	return fmt.Sprintf("%.0f", x)
}

// Tag is the stored form of a mark ID, for carriers that hold text rather
// than bits (document properties, a custom XML part): the ID and a keyed
// check value, so an edited or invented tag does not verify.
func (k Key) Tag(id uint16) string {
	return fmt.Sprintf("%s.%x", FormatID(id), k.Sum("meta", strconv.Itoa(int(id)))[:3])
}

// ParseTag reads a stored tag back. ok reports whether the text has the shape
// of a tag at all; valid whether its check value verifies under this key.
func (k Key) ParseTag(tag string) (id uint16, ok, valid bool) {
	tag = strings.TrimSpace(tag)
	var check string
	if n, err := fmt.Sscanf(tag, "MK-%04X.%s", &id, &check); n != 2 || err != nil {
		return 0, false, false
	}
	return id, true, k.Tag(id) == tag
}

// Plural renders a count with the matching noun and thousands separators,
// e.g. "1 page" or "1,650 words", for text a person reads.
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s + " " + many
}
