package pathtpl

import (
	"testing"
	"time"
)

func TestFormatDate(t *testing.T) {
	d := time.Date(2025, 7, 5, 9, 4, 3, 0, time.UTC) // a Saturday
	cases := []struct{ pattern, locale, want string }{
		{"yyyy", "de", "2025"},
		{"yy", "de", "25"},
		{"MM MMMM", "de", "07 Juli"},
		{"M. MMM", "de", "7. Jul"},
		{"MMMM", "en", "July"},
		{"MMMM", "fr", "juillet"},
		{"dd.MM.yyyy", "de", "05.07.2025"},
		{"d", "de", "5"},
		{"EEEE", "de", "Samstag"},
		{"EEE", "en", "Sat"},
		{"QQQ", "de", "Q3"},
		{"Q", "de", "3"},
		{"'KW' ww", "de", "KW 27"},
		{"HH:mm:ss", "de", "09:04:03"},
		{"'It''s' yyyy", "en", "It's 2025"},
		{"MMMM", "xx", "Juli"}, // unknown: German
	}
	for _, c := range cases {
		if got := FormatDate(d, c.pattern, c.locale); got != c.want {
			t.Errorf("FormatDate(%q, %s) = %q, want %q", c.pattern, c.locale, got, c.want)
		}
	}
}

func TestRender(t *testing.T) {
	d := time.Date(2019, 5, 28, 12, 0, 0, 0, time.UTC)
	text := map[string]string{"ort": "Lübbecke", "kategorie": "", "kamera": "Canon/EOS: 5D"}
	v := Values{Date: d, Locale: "de", Text: func(f string) string { return text[f] }}
	cases := []struct{ tpl, want string }{
		{"{jahr}/{datum:MM MMMM} {ort}", "2019/05 Mai Lübbecke"},
		{"{year}/{month} {place}", "2019/05 Lübbecke"},
		{"Dokumente/{datum:yyyy}/{datum:MM MMMM} {kategorie}", "Dokumente/2019/05 Mai"},
		{"Dokumente/{jahr}/{datum:MM MMMM} {kategorie|Sonstiges}", "Dokumente/2019/05 Mai Sonstiges"},
		{"{kamera}", "Canon-EOS- 5D"},
		{"{kategorie}/{jahr}", "2019"},
		{"../{jahr}/.versteckt/ x .", "2019/versteckt/x"},
		{"{{{jahr}}}", "{2019}"},
	}
	for _, c := range cases {
		tp, err := Parse(c.tpl)
		if err != nil {
			t.Fatalf("%q: %v", c.tpl, err)
		}
		if got := tp.Render(v); got != c.want {
			t.Errorf("%q = %q, want %q", c.tpl, got, c.want)
		}
	}
	// no date: the fallback
	tp, _ := Parse("{jahr|Ohne Datum}/{ort}")
	if got := tp.Render(Values{Text: v.Text}); got != "Ohne Datum/Lübbecke" {
		t.Errorf("no date: %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"", "{jahr", "jahr}", "{wetter}", "{ort:yyyy}"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
}
