// Package pathtpl turns metadata into folder paths for the filing rules.
//
// A template is text with fields in braces:
//
//	{jahr}/{datum:MM MMMM} {ort|Unbekannt}
//	Dokumente/{datum:yyyy}/{datum:MM MMMM} {kategorie}
//
// {field} inserts a value, {field:format} formats a date with Unicode
// (CLDR/ICU) date symbols such as yyyy, MM, MMMM, dd or EEEE, and
// {field|text} gives text to use when the value is empty. Values never
// create folders of their own: a "/" in a place name becomes "-". "{{" and
// "}}" are literal braces.
package pathtpl

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Field describes one field of a template.
type Field struct {
	Name    string   `json:"name"`    // canonical (German) name
	Aliases []string `json:"aliases"` // other names, English included
	Date    bool     `json:"date"`    // takes a date format
	Default string   `json:"default"` // date format when none is given
}

// Fields lists what templates can use. The names are German like the
// search syntax; the English names work as well.
var Fields = []Field{
	{Name: "datum", Aliases: []string{"date"}, Date: true, Default: "yyyy-MM-dd"},
	{Name: "jahr", Aliases: []string{"year"}, Date: true, Default: "yyyy"},
	{Name: "monat", Aliases: []string{"month"}, Date: true, Default: "MM"},
	{Name: "ort", Aliases: []string{"place"}},
	{Name: "kategorie", Aliases: []string{"category"}},
	{Name: "album", Aliases: []string{"folder", "ordner"}},
	{Name: "kamera", Aliases: []string{"camera"}},
	{Name: "typ", Aliases: []string{"kind", "type"}},
	{Name: "schlagwort", Aliases: []string{"keyword"}},
	{Name: "person", Aliases: []string{"people", "personen"}},
}

var fieldByName = func() map[string]*Field {
	m := map[string]*Field{}
	for i := range Fields {
		f := &Fields[i]
		m[f.Name] = f
		for _, a := range f.Aliases {
			m[a] = f
		}
	}
	return m
}()

type part struct {
	lit      string
	field    *Field
	format   string
	fallback string
}

// Template is a parsed path template.
type Template struct {
	src   string
	parts []part
}

// String returns the template as written.
func (t *Template) String() string { return t.src }

// Parse reads a template.
func Parse(src string) (*Template, error) {
	t := &Template{src: src}
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("leerer Pfad")
	}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t.parts = append(t.parts, part{lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '{' && i+1 < len(src) && src[i+1] == '{':
			lit.WriteByte('{')
			i++
		case c == '}' && i+1 < len(src) && src[i+1] == '}':
			lit.WriteByte('}')
			i++
		case c == '}':
			return nil, fmt.Errorf("„}“ ohne „{“ an Stelle %d", i+1)
		case c == '{':
			end := strings.IndexByte(src[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("„{“ ohne „}“ an Stelle %d", i+1)
			}
			p, err := parseField(src[i+1 : i+end])
			if err != nil {
				return nil, err
			}
			flush()
			t.parts = append(t.parts, p)
			i += end
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return t, nil
}

func parseField(s string) (part, error) {
	var p part
	name := s
	if i := strings.IndexByte(name, '|'); i >= 0 {
		name, p.fallback = name[:i], name[i+1:]
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name, p.format = name[:i], name[i+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	f := fieldByName[name]
	if f == nil {
		return p, fmt.Errorf("unbekanntes Feld „%s“", name)
	}
	p.field = f
	if p.format != "" && !f.Date {
		return p, fmt.Errorf("„%s“ ist kein Datum und hat kein Format", f.Name)
	}
	if f.Date && p.format == "" {
		p.format = f.Default
	}
	return p, nil
}

// Values supplies the metadata of one file.
type Values struct {
	Date   time.Time // zero: unknown
	Locale string    // language of month and weekday names
	Text   func(field string) string
}

// Render fills the template and returns a clean relative folder path with
// "/" separators ("" for the top folder).
func (t *Template) Render(v Values) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.field == nil {
			b.WriteString(p.lit)
			continue
		}
		var s string
		if p.field.Date {
			if !v.Date.IsZero() {
				s = FormatDate(v.Date, p.format, v.Locale)
			}
		} else if v.Text != nil {
			s = v.Text(p.field.Name)
		}
		s = cleanValue(s)
		if s == "" {
			s = cleanValue(p.fallback)
		}
		b.WriteString(s)
	}
	return CleanPath(b.String())
}

// cleanValue keeps a value inside one folder name.
func cleanValue(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			return '-'
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// CleanPath tidies a rendered path: no empty or dot-only folders, no
// leading dots (hidden folders), no trailing dots or spaces (Windows and
// SMB shares drop them), single spaces.
func CleanPath(p string) string {
	var out []string
	for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		seg = strings.Join(strings.Fields(seg), " ")
		seg = strings.TrimLeft(seg, ". -_")
		seg = strings.TrimRight(seg, ". -_")
		if seg == "" {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// --- dates ------------------------------------------------------------------

// FormatDate formats t with CLDR date symbols: y yy yyyy (year), M MM
// (month number), MMM MMMM (month name, also L), d dd (day), E EEE EEEE
// (weekday), Q QQ QQQ (quarter), w ww (ISO week), H HH m mm s ss (time).
// Text in single quotes is copied as is (” is a quote); other characters
// are copied too.
func FormatDate(t time.Time, pattern, locale string) string {
	loc := localeOf(locale)
	var b strings.Builder
	rs := []rune(pattern)
	for i := 0; i < len(rs); {
		c := rs[i]
		if c == '\'' {
			if i+1 < len(rs) && rs[i+1] == '\'' {
				b.WriteRune('\'')
				i += 2
				continue
			}
			j := i + 1
			for j < len(rs) {
				if rs[j] == '\'' {
					if j+1 < len(rs) && rs[j+1] == '\'' { // '' inside quotes
						b.WriteRune('\'')
						j += 2
						continue
					}
					break
				}
				b.WriteRune(rs[j])
				j++
			}
			i = j + 1
			continue
		}
		n := 1
		for i+n < len(rs) && rs[i+n] == c {
			n++
		}
		i += n
		switch c {
		case 'y', 'u':
			if n == 2 {
				fmt.Fprintf(&b, "%02d", t.Year()%100)
			} else {
				fmt.Fprintf(&b, "%0*d", max(n, 1), t.Year())
			}
		case 'M', 'L':
			switch {
			case n >= 4:
				b.WriteString(loc.months[t.Month()-1])
			case n == 3:
				b.WriteString(loc.monthsShort[t.Month()-1])
			case n == 2:
				fmt.Fprintf(&b, "%02d", int(t.Month()))
			default:
				fmt.Fprintf(&b, "%d", int(t.Month()))
			}
		case 'd':
			fmt.Fprintf(&b, "%0*d", min(n, 2), t.Day())
		case 'E', 'c':
			if n >= 4 {
				b.WriteString(loc.days[t.Weekday()])
			} else {
				b.WriteString(loc.daysShort[t.Weekday()])
			}
		case 'Q', 'q':
			q := (int(t.Month())-1)/3 + 1
			switch {
			case n >= 3:
				fmt.Fprintf(&b, "Q%d", q)
			default:
				fmt.Fprintf(&b, "%0*d", n, q)
			}
		case 'w':
			_, w := t.ISOWeek()
			fmt.Fprintf(&b, "%0*d", min(n, 2), w)
		case 'H':
			fmt.Fprintf(&b, "%0*d", min(n, 2), t.Hour())
		case 'm':
			fmt.Fprintf(&b, "%0*d", min(n, 2), t.Minute())
		case 's':
			fmt.Fprintf(&b, "%0*d", min(n, 2), t.Second())
		default:
			for range n {
				b.WriteRune(c)
			}
		}
	}
	return b.String()
}

// --- locales ----------------------------------------------------------------

type locale struct {
	name                 string
	months, monthsShort  [12]string
	days, daysShort      [7]string // Sunday first, like time.Weekday
	photo, video, docKey string
}

// Locale is a language for month and weekday names.
type Locale struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

var locales = map[string]*locale{
	"de": {name: "Deutsch",
		months:      [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		monthsShort: [12]string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"},
		days:        [7]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
		daysShort:   [7]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"},
		photo:       "Foto", video: "Video", docKey: "Dokument"},
	"en": {name: "English",
		months:      [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		monthsShort: [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
		days:        [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
		daysShort:   [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
		photo:       "Photo", video: "Video", docKey: "Document"},
	"fr": {name: "Français",
		months:      [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
		monthsShort: [12]string{"janv", "févr", "mars", "avr", "mai", "juin", "juil", "août", "sept", "oct", "nov", "déc"},
		days:        [7]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
		daysShort:   [7]string{"dim", "lun", "mar", "mer", "jeu", "ven", "sam"},
		photo:       "Photo", video: "Vidéo", docKey: "Document"},
	"es": {name: "Español",
		months:      [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
		monthsShort: [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
		days:        [7]string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
		daysShort:   [7]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"},
		photo:       "Foto", video: "Vídeo", docKey: "Documento"},
	"it": {name: "Italiano",
		months:      [12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
		monthsShort: [12]string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},
		days:        [7]string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
		daysShort:   [7]string{"dom", "lun", "mar", "mer", "gio", "ven", "sab"},
		photo:       "Foto", video: "Video", docKey: "Documento"},
	"nl": {name: "Nederlands",
		months:      [12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"},
		monthsShort: [12]string{"jan", "feb", "mrt", "apr", "mei", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},
		days:        [7]string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"},
		daysShort:   [7]string{"zo", "ma", "di", "wo", "do", "vr", "za"},
		photo:       "Foto", video: "Video", docKey: "Document"},
	"da": {name: "Dansk",
		months:      [12]string{"januar", "februar", "marts", "april", "maj", "juni", "juli", "august", "september", "oktober", "november", "december"},
		monthsShort: [12]string{"jan", "feb", "mar", "apr", "maj", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},
		days:        [7]string{"søndag", "mandag", "tirsdag", "onsdag", "torsdag", "fredag", "lørdag"},
		daysShort:   [7]string{"søn", "man", "tir", "ons", "tor", "fre", "lør"},
		photo:       "Foto", video: "Video", docKey: "Dokument"},
	"sv": {name: "Svenska",
		months:      [12]string{"januari", "februari", "mars", "april", "maj", "juni", "juli", "augusti", "september", "oktober", "november", "december"},
		monthsShort: [12]string{"jan", "feb", "mars", "apr", "maj", "juni", "juli", "aug", "sep", "okt", "nov", "dec"},
		days:        [7]string{"söndag", "måndag", "tisdag", "onsdag", "torsdag", "fredag", "lördag"},
		daysShort:   [7]string{"sön", "mån", "tis", "ons", "tors", "fre", "lör"},
		photo:       "Foto", video: "Video", docKey: "Dokument"},
	"pl": {name: "Polski",
		months:      [12]string{"styczeń", "luty", "marzec", "kwiecień", "maj", "czerwiec", "lipiec", "sierpień", "wrzesień", "październik", "listopad", "grudzień"},
		monthsShort: [12]string{"sty", "lut", "mar", "kwi", "maj", "cze", "lip", "sie", "wrz", "paź", "lis", "gru"},
		days:        [7]string{"niedziela", "poniedziałek", "wtorek", "środa", "czwartek", "piątek", "sobota"},
		daysShort:   [7]string{"niedz", "pon", "wt", "śr", "czw", "pt", "sob"},
		photo:       "Zdjęcie", video: "Wideo", docKey: "Dokument"},
	"pt": {name: "Português",
		months:      [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
		monthsShort: [12]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"},
		days:        [7]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"},
		daysShort:   [7]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"},
		photo:       "Foto", video: "Vídeo", docKey: "Documento"},
}

// LocaleCodes in the order shown.
var LocaleCodes = []string{"de", "en", "fr", "es", "it", "nl", "da", "sv", "pl", "pt"}

// Locales lists the languages for month and weekday names.
func Locales() []Locale {
	var out []Locale
	for _, c := range LocaleCodes {
		out = append(out, Locale{c, locales[c].name})
	}
	return out
}

// Supported reports whether a locale code is known ("" counts: German).
func Supported(code string) bool {
	_, ok := locales[code]
	return ok || code == ""
}

func localeOf(code string) *locale {
	if l := locales[code]; l != nil {
		return l
	}
	return locales["de"]
}

// KindName is the word for photo, video or document in a locale.
func KindName(code, kind string) string {
	l := localeOf(code)
	switch kind {
	case "video":
		return l.video
	case "doc":
		return l.docKey
	}
	return l.photo
}
