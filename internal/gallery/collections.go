package gallery

import (
	"sort"
	"strings"
	"time"
)

// Place groups items by the place of their folder.
type Place struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Cover string  `json:"cover"`
	From  int64   `json:"from"`
	To    int64   `json:"to"`
	Lat   float64 `json:"lat,omitempty"`
	Lon   float64 `json:"lon,omitempty"`
}

// Places returns all places, most items first.
func (l *Library) Places() []Place {
	m := map[string]*Place{}
	gps := map[string]int{}
	for _, it := range l.Items() {
		if it.Place == "" || it.LivePhoto != "" {
			continue
		}
		p := m[it.Place]
		if p == nil {
			p = &Place{Name: it.Place, Cover: it.ID, From: it.Taken, To: it.Taken}
			m[it.Place] = p
		}
		p.Count++
		p.From = min(p.From, it.Taken)
		p.To = max(p.To, it.Taken)
		if it.GPS {
			p.Lat += it.Lat
			p.Lon += it.Lon
			gps[it.Place]++
		}
	}
	out := make([]Place, 0, len(m))
	for name, p := range m {
		if n := gps[name]; n > 0 {
			p.Lat /= float64(n)
			p.Lon /= float64(n)
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Count != out[b].Count {
			return out[a].Count > out[b].Count
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// Album is one folder with its sub folders.
type Album struct {
	Path   string   `json:"path"` // "<root>/<folder>"
	Name   string   `json:"name"`
	Count  int      `json:"count"` // items directly inside
	Total  int      `json:"total"` // including sub folders
	Cover  string   `json:"cover"`
	From   int64    `json:"from"`
	To     int64    `json:"to"`
	Albums []*Album `json:"albums,omitempty"`
}

// Albums returns the folder tree of every root.
func (l *Library) Albums() []*Album {
	nodes := map[string]*Album{}
	var roots []*Album
	var get func(p string) *Album
	get = func(p string) *Album {
		if a := nodes[p]; a != nil {
			return a
		}
		i := strings.LastIndexByte(p, '/')
		a := &Album{Path: p, Name: p[i+1:]}
		nodes[p] = a
		if i < 0 {
			roots = append(roots, a)
		} else {
			parent := get(p[:i])
			parent.Albums = append(parent.Albums, a)
		}
		return a
	}
	for _, r := range l.opt.Roots {
		get(r.Name)
	}
	for _, it := range l.Items() {
		if it.LivePhoto != "" {
			continue
		}
		p := l.opt.Roots[it.Root].Name
		if it.Album != "" {
			p += "/" + it.Album
		}
		a := get(p)
		a.Count++
		for x := a; x != nil; {
			x.Total++
			if x.Cover == "" {
				x.Cover = it.ID // items are newest first
				x.To = it.Taken
			}
			if x.From == 0 || it.Taken < x.From {
				x.From = it.Taken
			}
			i := strings.LastIndexByte(x.Path, '/')
			if i < 0 {
				break
			}
			x = nodes[x.Path[:i]]
		}
	}
	var sortRec func([]*Album)
	sortRec = func(as []*Album) {
		sort.Slice(as, func(a, b int) bool { return as[a].Name > as[b].Name })
		for _, a := range as {
			sortRec(a.Albums)
		}
	}
	for _, r := range roots {
		sortRec(r.Albums)
	}
	return roots
}

// AlbumOf returns "<root>/<folder>" of an item.
func (l *Library) AlbumOf(it *Item) string {
	p := l.opt.Roots[it.Root].Name
	if it.Album != "" {
		p += "/" + it.Album
	}
	return p
}

// MonthName returns the German month name.
func MonthName(m time.Month) string { return monthsDE[m] }
