package codec

import "testing"

func TestTagRoundTrip(t *testing.T) {
	k := NewKey()
	tag := k.Tag(0x5DA6)
	id, ok, valid := k.ParseTag(tag)
	if !ok || !valid || id != 0x5DA6 {
		t.Fatalf("%q: id %04X ok %v valid %v", tag, id, ok, valid)
	}
	if _, ok, valid := k.ParseTag("MK-5DA6.000000"); !ok || valid {
		t.Errorf("forged check: ok %v valid %v", ok, valid)
	}
	if _, ok, _ := k.ParseTag("not a tag"); ok {
		t.Error("free text parsed as a tag")
	}
	if _, _, valid := NewKey().ParseTag(tag); valid {
		t.Error("a tag verified under another key")
	}
}

func TestReconcile(t *testing.T) {
	a := func(layer string, s Strength) Claim {
		return Claim{Layer: layer, MarkID: "MK-000A", Recipient: "A", Strength: s}
	}
	b := func(layer string, s Strength) Claim {
		return Claim{Layer: layer, MarkID: "MK-000B", Recipient: "B", Strength: s}
	}
	cases := []struct {
		name     string
		named    string
		fromBits bool
		claims   []Claim
		conflict bool
	}{
		{"agreeing", "MK-000A", true, []Claim{a("bg", Attributed), a("xml", Stored)}, false},
		{"bits against planted tag", "MK-000A", true, []Claim{a("bg", Attributed), b("xml", Stored)}, true},
		// The QA case: only tags name B, and a bit carrier too weak to
		// attribute on its own still points at A.
		{"tag-only verdict, bits lean elsewhere", "MK-000B", false, []Claim{a("text", Leaning), b("xml", Stored), b("meta", Stored)}, true},
		{"bits verdict, one layer leans elsewhere", "MK-000A", true, []Claim{a("bg", Attributed), b("spacing", Leaning)}, false},
		{"nothing named, two leanings", "", false, []Claim{a("x", Leaning), b("y", Leaning)}, false},
	}
	for _, c := range cases {
		groups, conflict := Reconcile(c.named, c.fromBits, c.claims)
		if conflict != c.conflict {
			t.Errorf("%s: conflict %v, want %v (%+v)", c.name, conflict, c.conflict, groups)
		}
	}
	groups, _ := Reconcile("MK-000A", true, []Claim{b("xml", Stored), a("bg", Attributed), a("text", Leaning)})
	if groups[0].Recipient != "A" || len(groups[0].Layers) != 2 || groups[0].Strength != "attributed" {
		t.Errorf("strongest candidate first: %+v", groups)
	}
	if got := Agreeing("MK-000A", groups); len(got) != 2 {
		t.Errorf("agreeing %v", got)
	}
}
