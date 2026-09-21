package tabular

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"attribution/common/codec"
)

func issue(t *testing.T, master *Table, sc Schema, names ...string) *Registry {
	t.Helper()
	reg := &Registry{Key: codec.NewKey(), Master: master, Schema: sc, Copies: map[uint16]*Table{}}
	all := Techniques{Canary: true, LowBit: true, Dummy: true}
	for _, n := range names {
		var ids []uint16
		for _, is := range reg.Issued {
			ids = append(ids, is.MarkID)
		}
		id := reg.Key.AllocateID(ids)
		cp, is := MarkCopy(reg.Key, master, sc, id, all)
		is.Recipient = n
		reg.Issued = append(reg.Issued, is)
		reg.Copies[id] = cp
	}
	return reg
}

// battery runs every attack against one recipient's copy and reports, per
// technique, whether it identified the right recipient.
func battery(t *testing.T, reg *Registry, leaker *Issuance) {
	t.Helper()
	for i, b := range Battery(reg.Schema) {
		b.Attack.Seed = uint64(i + 1)
		rep := reg.Detect(Apply(reg.Copies[leaker.MarkID], reg.Schema, b.Attack))
		var line []string
		for _, r := range rep.Results {
			mark := "-"
			switch {
			case r.Status == "attributed" && r.MarkID == leaker.Mark:
				mark = "OK"
			case r.Status == "attributed":
				mark = "WRONG"
				t.Errorf("%s: %s named the wrong recipient: %s", b.Name, r.Technique, r.Detail)
			case r.Status == "inconclusive":
				mark = "~"
			}
			line = append(line, r.Technique+"="+mark)
		}
		t.Logf("%-38s %s | %s", b.Name, strings.Join(line, " "), rep.Verdict.Status)
	}
	if rep := reg.Detect(reg.Master); rep.Verdict.Status == "attributed" {
		t.Errorf("the unmarked source was attributed: %+v", rep.Verdict)
	}
}

func TestSampleTable(t *testing.T) {
	master := Generate(2000, 7)
	sc, cols := DetectSchema(master)
	t.Logf("detected: %s, dummy column %s", sc.Describe(), sc.Dummy)
	for _, c := range cols {
		t.Logf("  %-12s %-9s unique=%.2f decimals=%d tolerance=%q", c.Name, c.Kind, c.Unique, c.Decimals, c.Tolerance)
	}
	if sc.Key != "account_id" {
		t.Errorf("expected account_id as the identifying column, got %q", sc.Key)
	}
	var tolerant []string
	for _, f := range sc.Tolerant {
		tolerant = append(tolerant, f.Name)
	}
	for _, want := range []string{"opened_at", "latitude", "longitude", "risk_score"} {
		if !strings.Contains(strings.Join(tolerant, ","), want) {
			t.Errorf("expected %s to be detected as tolerant, got %v", want, tolerant)
		}
	}
	if strings.Contains(strings.Join(tolerant, ","), "balance_eur") {
		t.Error("balance_eur has only 2 decimals and must not be marked")
	}
	reg := issue(t, master, sc, "Northwind", "Blau", "Internal Risk", "Tessin Audit", "Datenwerk")
	battery(t, reg, reg.Issued[2])
}

// A table with unfamiliar column names, to check that nothing is hardcoded.
func TestForeignTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	master := &Table{Columns: []string{"order_ref", "buyer", "sku", "net_price", "lat", "lng", "placed_at"}}
	for i := 0; i < 800; i++ {
		master.Rows = append(master.Rows, []string{
			fmt.Sprintf("ORD-%07d", 1000000+rng.IntN(8000000)),
			fmt.Sprintf("buyer%03d@example.com", rng.IntN(400)),
			fmt.Sprintf("SKU-%04d", rng.IntN(900)),
			fmt.Sprintf("%.2f", 5+rng.Float64()*300),
			formatFixed(int64(47000000+rng.IntN(900000)), 6),
			formatFixed(int64(8000000+rng.IntN(900000)), 6),
			fmt.Sprintf("2026-0%d-1%d 0%d:2%d:1%d.%03d", 1+rng.IntN(8), rng.IntN(9), rng.IntN(9), rng.IntN(9), rng.IntN(9), rng.IntN(1000)),
		})
	}
	sc, _ := DetectSchema(master)
	t.Logf("detected: %s", sc.Describe())
	if sc.Key != "order_ref" {
		t.Errorf("expected order_ref as the identifying column, got %q", sc.Key)
	}
	if err := sc.Validate(master); err != nil {
		t.Fatalf("schema not valid: %v", err)
	}
	reg := issue(t, master, sc, "Vendor A", "Vendor B", "Vendor C")
	leaker := reg.Issued[1]
	cp := reg.Copies[leaker.MarkID]
	if len(cp.Rows) <= len(master.Rows) {
		t.Error("canary rows were not added")
	}
	if cp.Col(sc.Dummy) < 0 {
		t.Error("dummy column was not added")
	}
	// Canary rows must not duplicate a real identity.
	seen := map[string]bool{}
	for _, row := range master.Rows {
		seen[row[master.Col("order_ref")]] = true
	}
	for _, c := range leaker.Canaries {
		if seen[c.Ident["order_ref"]] {
			t.Errorf("canary reuses a real order_ref: %v", c.Ident)
		}
	}
	battery(t, reg, leaker)
}

// A table with no unique column at all: rows are recognised by their contents.
func TestTableWithoutKey(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	master := &Table{Columns: []string{"region", "segment", "score", "updated_at"}}
	for i := 0; i < 600; i++ {
		master.Rows = append(master.Rows, []string{
			[]string{"north", "south", "east", "west"}[rng.IntN(4)],
			[]string{"retail", "wholesale", "online"}[rng.IntN(3)],
			formatFixed(int64(rng.IntN(1000000)), 4),
			fmt.Sprintf("2026-03-1%d 1%d:0%d:0%d.%03d", rng.IntN(9), rng.IntN(9), rng.IntN(5), rng.IntN(9), rng.IntN(1000)),
		})
	}
	sc, _ := DetectSchema(master)
	if sc.Key != "" {
		t.Logf("note: %q was unique enough to act as the identifying column", sc.Key)
	}
	if err := sc.Validate(master); err != nil {
		t.Fatalf("schema not valid: %v", err)
	}
	reg := issue(t, master, sc, "Partner A", "Partner B")
	rep := reg.Detect(reg.Copies[reg.Issued[0].MarkID])
	if rep.Verdict.Status != "attributed" || rep.Verdict.Recipient != "Partner A" {
		t.Errorf("verbatim copy not identified: %+v", rep.Verdict)
	}
	t.Logf("no-key table: %s, %s", rep.Verdict.Detail, rep.Results[2].Detail)
}
