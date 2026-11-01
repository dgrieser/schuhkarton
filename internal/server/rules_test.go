package server

import (
	"encoding/json"
	"testing"
)

func TestRulesAPI(t *testing.T) {
	f := newFixture(t, "", true)
	anna := f.login("Anna", "admin-pass")
	body := `{"locale":"en","auto":true,"list":[{"name":"Fotos","when":"","path":"{year}/{date:MMMM}"}]}`
	var prev map[string]any
	json.Unmarshal(f.do("POST", "/api/admin/rules/preview", body, anna).Body.Bytes(), &prev)
	if rules, _ := prev["rules"].([]any); len(rules) != 1 {
		t.Fatalf("preview: %v", prev)
	}
	// a broken template is refused and not used
	var res map[string]any
	json.Unmarshal(f.do("POST", "/api/admin/rules", `{"list":[{"path":"{jahr"}]}`, anna).Body.Bytes(), &res)
	if res["ok"] == true {
		t.Fatalf("bad rule saved: %v", res)
	}
	// a rule without folder is refused, not silently dropped
	res = nil
	json.Unmarshal(f.do("POST", "/api/admin/rules", `{"list":[{"name":"x","when":"ort:Berlin","path":" "}]}`, anna).Body.Bytes(), &res)
	if res["ok"] == true || res["error"] != "Regel 1: Ordner fehlt" {
		t.Fatalf("blank rule: %v", res)
	}
	res = nil
	json.Unmarshal(f.do("POST", "/api/admin/rules", body, anna).Body.Bytes(), &res)
	if res["ok"] != true {
		t.Fatalf("save: %v", res)
	}
	var got struct {
		Rules struct {
			Locale string `json:"locale"`
			List   []struct {
				Path string `json:"path"`
			} `json:"list"`
		} `json:"rules"`
	}
	json.Unmarshal(f.do("GET", "/api/admin/rules", "", anna).Body.Bytes(), &got)
	if got.Rules.Locale != "en" || len(got.Rules.List) != 1 || got.Rules.List[0].Path != "{year}/{date:MMMM}" {
		t.Fatalf("rules after save: %+v", got)
	}
	if f.cfg.Library.Rules.Locale != "en" || len(f.cfg.Library.Rules.List) != 1 {
		t.Fatalf("config not updated: %+v", f.cfg.Library.Rules)
	}
}
