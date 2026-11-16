package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// LogoChanges are the values of ui.logo_change: how often the logo turns
// to the next colour when ui.logo names several.
var LogoChanges = []string{"visit", "hourly", "daily", "weekly"}

// LogoList is ui.logo: one colour of Logos, or several that take turns.
// In the file it is a name ("aqua") or a list; from the environment or a
// flag the names are separated by commas.
type LogoList []string

// UnmarshalYAML takes a single name as well as a list.
func (l *LogoList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = splitList(n.Value)
		return nil
	}
	var s []string
	if err := n.Decode(&s); err != nil {
		return err
	}
	*l = s
	return nil
}

// MarshalYAML writes one colour as a name, several as a list.
func (l LogoList) MarshalYAML() (any, error) {
	if len(l) == 1 {
		return l[0], nil
	}
	return []string(l), nil
}

// UnmarshalJSON takes a single name as well as a list (the settings page sends a list).
func (l *LogoList) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = splitList(s)
		return nil
	}
	var a []string
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*l = a
	return nil
}

// normLogos cleans ui.logo and ui.logo_change: lower case, no doubles, the
// defaults when empty.
func (u *UI) normLogos(d UI) {
	var l LogoList
	for _, n := range u.Logo {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && !slices.Contains(l, n) {
			l = append(l, n)
		}
	}
	if len(l) == 0 {
		l = append(LogoList(nil), d.Logo...)
	}
	u.Logo = l
	u.LogoChange = strings.ToLower(strings.TrimSpace(cmpOr(u.LogoChange, d.LogoChange)))
}

func (u *UI) logoErrors() []string {
	var errs []string
	for _, n := range u.Logo {
		if !slices.Contains(Logos, n) {
			errs = append(errs, fmt.Sprintf("ui.logo %q: %s", n, strings.Join(Logos, ", ")))
		}
	}
	if !slices.Contains(LogoChanges, u.LogoChange) {
		errs = append(errs, fmt.Sprintf("ui.logo_change %q: %s", u.LogoChange, strings.Join(LogoChanges, ", ")))
	}
	return errs
}
