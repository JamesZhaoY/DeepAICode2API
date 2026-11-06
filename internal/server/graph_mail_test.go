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
	info, err := os.Stat(s.graphMailPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	r = do(t, s, http.MethodPost, "/admin/api/register/graph-mail", "", `{"content":"invalid"}`)
	if r.Code != 400 {
		t.Fatal("expected validation error")
	}
	if (*get())["count"] != float64(2) {
		t.Fatal("invalid import replaced file")
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
