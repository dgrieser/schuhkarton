package server

import (
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// photoFixture is a server over a library with one photo.
func photoFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "photos")
	os.MkdirAll(root, 0o755)
	f, _ := os.Create(filepath.Join(root, "a.jpg"))
	jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil)
	f.Close()
	lib, err := gallery.Open(gallery.Options{Roots: []gallery.Root{{Name: "Bilder", Path: root}}, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lib.Close)
	if err := lib.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	users, _ := auth.Open(filepath.Join(dir, "users.json"))
	users.Create("Anna", "admin-pass", auth.RoleAdmin, false)
	users.Create("Ben", "user-pass", auth.RoleUser, false)
	users.Create("Cleo", "user-pass2", auth.RoleUser, false)
	cfg := config.Defaults()
	h := New(context.Background(), Options{Lib: lib, Config: cfg, Users: users})
	return &fixture{t: t, h: h, cfg: cfg, users: users}, lib.Items()[0].ID
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return m
}

func TestMarksAPI(t *testing.T) {
	f, id := photoFixture(t)
	ben, cleo, anna := f.login("Ben", "user-pass"), f.login("Cleo", "user-pass2"), f.login("Anna", "admin-pass")
	if got := f.do("POST", "/api/marks", `{"ids":["`+id+`"],"on":true}`, "").Code; got != 401 {
		t.Errorf("anonymous mark: %d", got)
	}
	if m := decode(t, f.do("POST", "/api/marks", `{"ids":["`+id+`"],"on":true}`, ben).Body.String()); m["ok"] != true {
		t.Fatalf("mark: %v", m)
	}
	// hidden for Ben only
	marked := func(session string) bool {
		var res struct {
			Items []struct {
				ID string `json:"id"`
				Mk bool   `json:"mk"`
			} `json:"items"`
		}
		json.Unmarshal(f.do("GET", "/api/items", "", session).Body.Bytes(), &res)
		return len(res.Items) == 1 && res.Items[0].Mk
	}
	if !marked(ben) || marked(cleo) || marked("") {
		t.Error("the mark must only hide the photo for Ben")
	}
	if got := f.do("GET", "/api/admin/marks", "", ben).Code; got != 403 {
		t.Errorf("user sees all marks: %d", got)
	}
	all := f.do("GET", "/api/admin/marks", "", anna).Body.String()
	if !strings.Contains(all, `"user":"Ben"`) {
		t.Errorf("admin marks: %s", all)
	}
	if m := decode(t, f.do("POST", "/api/admin/marks/clear", `{"ids":["`+id+`"]}`, anna).Body.String()); m["changed"] != float64(1) {
		t.Errorf("clear: %v", m)
	}
	if marked(ben) {
		t.Error("still marked after the admin cleared it")
	}
}

func TestNotesAPI(t *testing.T) {
	f, id := photoFixture(t)
	ben, cleo, anna := f.login("Ben", "user-pass"), f.login("Cleo", "user-pass2"), f.login("Anna", "admin-pass")
	if got := f.do("POST", "/api/notes/"+id, `{"text":"hallo"}`, "").Code; got != 401 {
		t.Errorf("anonymous note: %d", got)
	}
	m := decode(t, f.do("POST", "/api/notes/"+id, `{"text":"Kindergeburtstag bei Tante Ilse"}`, ben).Body.String())
	if m["ok"] != true {
		t.Fatalf("add: %v", m)
	}
	nid := m["note"].(map[string]any)["id"].(string)
	// everybody reads it, also without an account
	if d := f.do("GET", "/api/item/"+id, "", "").Body.String(); !strings.Contains(d, "Tante Ilse") {
		t.Errorf("anonymous viewers do not see the note: %s", d)
	}
	if m := decode(t, f.do("PATCH", "/api/notes/"+id+"/"+nid, `{"text":"geändert"}`, cleo).Body.String()); m["ok"] == true {
		t.Error("Cleo changed Ben's note")
	}
	if m := decode(t, f.do("DELETE", "/api/notes/"+id+"/"+nid, "", cleo).Body.String()); m["ok"] == true {
		t.Error("Cleo deleted Ben's note")
	}
	if m := decode(t, f.do("PATCH", "/api/notes/"+id+"/"+nid, `{"text":"Kindergeburtstag bei Tante Ilse 1987"}`, ben).Body.String()); m["ok"] != true {
		t.Errorf("Ben edits his note: %v", m)
	}
	var hits struct {
		Hits []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	json.Unmarshal(f.do("GET", "/api/search?q=notiz:ilse", "", "").Body.Bytes(), &hits)
	if len(hits.Hits) != 1 || hits.Hits[0].ID != id {
		t.Errorf("search notiz:ilse: %+v", hits)
	}
	if m := decode(t, f.do("DELETE", "/api/notes/"+id+"/"+nid, "", anna).Body.String()); m["ok"] != true {
		t.Errorf("admin deletes any note: %v", m)
	}
}

func TestDupesAPIForAdmins(t *testing.T) {
	f, _ := photoFixture(t)
	ben, anna := f.login("Ben", "user-pass"), f.login("Anna", "admin-pass")
	for _, p := range []string{"/api/admin/dupes", "/api/admin/dupes?summary=1"} {
		if got := f.do("GET", p, "", ben).Code; got != 403 {
			t.Errorf("%s for a user: %d", p, got)
		}
		if got := f.do("GET", p, "", anna).Code; got != 200 {
			t.Errorf("%s for an admin: %d", p, got)
		}
	}
	if got := f.do("POST", "/api/admin/dupes/resolve", `{"groups":[]}`, ben).Code; got != 403 {
		t.Errorf("resolve for a user: %d", got)
	}
}
