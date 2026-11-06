package register

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type graphRoundTrip func(*http.Request) (*http.Response, error)

func (f graphRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGraphProviderAndVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.txt")
	if err := os.WriteFile(path, []byte("a@hotmail.com----password----client----refresh\nb@hotmail.com----password----client2----refresh2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	p.client = &http.Client{Transport: graphRoundTrip(func(r *http.Request) (*http.Response, error) {
		var body string
		if strings.Contains(r.URL.Host, "login.microsoftonline.com") {
			if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "refresh" {
				t.Fatal("invalid token request")
			}
			body = `{"access_token":"access","expires_in":3600}`
		} else {
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatal("missing token")
			}
			body = `{"value":[{"subject":"Verify","body":{"content":"https://api.deepai.org/verify_email?key=abcd-1234"}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	box, err := p.Create(context.Background())
	if err != nil || box.Email() != "a@hotmail.com" || box.Fresh() {
		t.Fatal("first account", err)
	}
	link, err := box.WaitForVerification(context.Background(), time.Now(), time.Second, time.Millisecond)
	if err != nil || link != "https://api.deepai.org/verify_email?key=abcd-1234" {
		t.Fatal(link, err)
	}
	box, err = p.Create(context.Background())
	if err != nil || box.Email() != "b@hotmail.com" {
		t.Fatal("second account", err)
	}
	if _, err = p.Create(context.Background()); err == nil {
		t.Fatal("expected exhaustion")
	}
	if _, ok := p.Inbox("A@HOTMAIL.COM"); !ok {
		t.Fatal("inbox lookup")
	}
}

func TestGraphReservationsPersistAcrossProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.txt")
	if err := os.WriteFile(path, []byte("a@example.com----pw----client----token\nb@example.com----pw----client----token\nc@example.com----pw----client----token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.MergeHistory(map[string]string{"A@example.com": "registered"}); err != nil {
		t.Fatal(err)
	}
	box, err := first.Create(context.Background())
	if err != nil || box.Email() != "b@example.com" {
		t.Fatalf("allocation: %v %v", box, err)
	}
	second, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := second.Statuses()
	if err != nil || statuses[0].Status != "registered" || statuses[1].Status != "pending" {
		t.Fatalf("statuses: %v %v", statuses, err)
	}
	if _, err := second.Reserve("a@example.com"); err == nil {
		t.Fatal("registered mailbox was reused")
	}
	if _, err := second.Reserve("b@example.com"); err == nil {
		t.Fatal("pending mailbox was reused")
	}
	box, err = second.Create(context.Background())
	if err != nil || box.Email() != "c@example.com" {
		t.Fatalf("second allocation: %v %v", box, err)
	}
	if err := box.(*graphInbox).MarkRegistered(); err != nil {
		t.Fatal(err)
	}
	statuses, err = first.Statuses()
	if err != nil || statuses[2].Status != "registered" {
		t.Fatalf("registered state: %v %v", statuses, err)
	}
}

func TestExplicitAvailableSurvivesHistoryAndCanBeReserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.txt")
	if err := os.WriteFile(path, []byte("a@example.com----pw----client----token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".state.json", []byte(`{"a@example.com":"available"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.MergeHistory(map[string]string{"a@example.com": "pending"}); err != nil {
		t.Fatal(err)
	}
	statuses, err := p.Statuses()
	if err != nil || statuses[0].Status != "available" {
		t.Fatalf("state: %v %v", statuses, err)
	}
	if _, err = p.Create(context.Background()); err != nil {
		t.Fatalf("reserve available account: %v", err)
	}
}
