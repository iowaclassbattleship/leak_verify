package tabular

import (
	"strings"
	"testing"
)

// A Swiss or German Excel export: a byte order mark, semicolons, and
// decimal commas.
func TestParseExcelSemicolonExport(t *testing.T) {
	src := "\uFEFFKunden-Nr;Vorname;Nachname;Saldo;Ort\r\n" +
		"2000;Luca;Keller;7951,14;Zürich\r\n" +
		"2001;Anna;Meier;-12,50;Bern\r\n" +
		"2002;Noah;Frei;1.204,00;Basel\r\n"
	tab, err := ParseCSV(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tab.Columns, "|"); got != "Kunden-Nr|Vorname|Nachname|Saldo|Ort" {
		t.Fatalf("columns %s", got)
	}
	if f := tab.Format; f.Delimiter != ";" || !f.DecimalComma || !f.BOM {
		t.Errorf("format %+v", f)
	}
	want := [][]string{
		{"2000", "Luca", "Keller", "7951.14", "Zürich"},
		{"2001", "Anna", "Meier", "-12.50", "Bern"},
		{"2002", "Noah", "Frei", "1204.00", "Basel"},
	}
	for i, row := range want {
		if strings.Join(tab.Rows[i], "|") != strings.Join(row, "|") {
			t.Errorf("row %d: %q", i, tab.Rows[i])
		}
	}
	// Written back the way it came in.
	out := string(tab.CSV())
	if !strings.HasPrefix(out, "\uFEFFKunden-Nr;Vorname") || !strings.Contains(out, "2000;Luca;Keller;7951,14;Zürich") {
		t.Errorf("round trip:\n%s", out)
	}
}

// Quoted fields holding the delimiter and newlines keep working, and do not
// confuse the delimiter guess.
func TestParseQuotedFields(t *testing.T) {
	src := "id,name,note\n1,\"Keller, Luca\",\"two\nlines\"\n2,\"Meier; Anna\",plain\n"
	tab, err := ParseCSV(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if tab.Format.Delimiter != "," || tab.Format.DecimalComma {
		t.Errorf("format %+v", tab.Format)
	}
	if tab.Rows[0][1] != "Keller, Luca" || tab.Rows[0][2] != "two\nlines" || tab.Rows[1][1] != "Meier; Anna" {
		t.Errorf("rows %q", tab.Rows)
	}
}

func TestParseTabAndPipe(t *testing.T) {
	for _, d := range []string{"\t", "|"} {
		src := strings.ReplaceAll("a,b,c\n1,2.5,x\n2,3.5,y\n", ",", d)
		tab, err := ParseCSV(strings.NewReader(src))
		if err != nil || tab.Format.Delimiter != d || len(tab.Columns) != 3 || tab.Rows[1][1] != "3.5" {
			t.Errorf("%q: %+v %v", d, tab, err)
		}
	}
}

// The override wins over the guess.
func TestParseOverride(t *testing.T) {
	tab, err := ParseCSVWith(strings.NewReader("a;b\n1,5;x\n"), Format{Delimiter: ";", DecimalComma: true})
	if err != nil || tab.Rows[0][0] != "1.5" {
		t.Fatalf("%+v %v", tab, err)
	}
	tab, _ = ParseCSVWith(strings.NewReader("a;b\n1,5;x\n"), Format{Delimiter: ";"})
	if tab.Rows[0][0] != "1,5" {
		t.Errorf("decimal comma applied although not asked for: %q", tab.Rows[0][0])
	}
}
