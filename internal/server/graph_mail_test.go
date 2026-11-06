package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestGraphMailImport(t *testing.T) {
	s := newEmptyServer(t)
	get := func() *map[string]any {
		t.Helper()
		r := do(t, s, http.MethodGet, "/admin/api/register/graph-mail", "", "")
		if r.Code != 200 {
			t.Fatalf("get: %s", r.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return &result
	}
	if (*get())["count"] != float64(0) {
		t.Fatal("expected empty")
	}
	payload := `{"content":"a@hotmail.com----password----client----refresh\nb@hotmail.com----password----client----refresh"}`
	r := do(t, s, http.MethodPost, "/admin/api/register/graph-mail", "", payload)
	if r.Code != 200 || strings.Contains(r.Body.String(), "refresh") {
		t.Fatalf("import: %s", r.Body.String())
	}
	if (*get())["count"] != float64(2) {
		t.Fatal("expected two accounts")
	}
	r = do(t, s, http.MethodPost, "/admin/api/register/graph-mail", "", `{"content":"A@HOTMAIL.COM----newpassword----client----newrefresh\nc@hotmail.com----password----client----refresh"}`)
	if r.Code != 200 {
		t.Fatalf("merge failed: %s", r.Body.String())
	}
	var merged map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if merged["count"] != float64(3) || merged["added"] != float64(1) || merged["updated"] != float64(1) {
		t.Fatalf("unexpected counts: %v", merged)
	}
	contents, err := os.ReadFile(s.graphMailPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "newrefresh") || !strings.Contains(string(contents), "b@hotmail.com") {
		t.Fatal("merge failed to preserve or update entries")
	}
	info, err := os.Stat(s.graphMailPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	r = do(t, s, http.MethodPost, "/admin/api/register/graph-mail", "", `{"content":"invalid"}`)
	if r.Code != 400 {
		t.Fatal("expected validation error")
	}
	if (*get())["count"] != float64(3) {
		t.Fatal("invalid import changed file")
	}
}

func TestRegisterRejectsUnsupportedBackend(t *testing.T) {
	s := newEmptyServer(t)
	for _, backend := range []string{"cf", "static", "none"} {
		body := `{"mail_backend":"` + backend + `","count":1}`
		r := do(t, s, http.MethodPost, "/admin/api/register", "", body)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("backend %s: expected 400, got %d", backend, r.Code)
		}
	}
}

func TestRegisterCountLimit(t *testing.T) {
	s := newEmptyServer(t)
	r := do(t, s, http.MethodPost, "/admin/api/register", "", `{"count":1001}`)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", r.Code)
	}
}
