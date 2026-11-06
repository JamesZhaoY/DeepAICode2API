package server

import (
	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/register"
	"path/filepath"
	"testing"
)

func TestEnrollRecordPreservesRegistrationIdentity(t *testing.T) {
	s := newEmptyServer(t)
	rec := &register.Record{Email: "identity@example.com", APIKey: "test-upstream-key", MachineID: "test-machine-identity", Fingerprint: "test-fingerprint", UserAgent: "test-agent", AcceptLanguage: "en-US", SecCHUA: "test-ch", Platform: "test-platform", TheosBalance: "1", TheosGranted: "1"}
	id, err := s.enrollRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Store.AccountCopy(id)
	if a == nil {
		t.Fatal("missing account")
	}
	if a.MachineID != rec.MachineID || a.Fingerprint != rec.Fingerprint || a.UserAgent != rec.UserAgent || a.AcceptLanguage != rec.AcceptLanguage || a.SecCHUA != rec.SecCHUA || a.Platform != rec.Platform {
		t.Fatal("registration identity was not preserved")
	}
}

func TestRestoreLedgerIdentitiesMatchesAccount(t *testing.T) {
	s := newEmptyServer(t)
	rec := &register.Record{Email: "identity@example.com", APIKey: "test-upstream-key", MachineID: "original-machine", TheosBalance: "1"}
	ledger := register.NewLedger(filepath.Join(filepath.Dir(s.Store.Path()), "exports", "register-ledger.jsonl"))
	if err := ledger.Append(rec); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	acct, err := deepai.AccountFromRegistration(deepai.Registration{Email: rec.Email, APIKey: rec.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	s.Store.UpsertAccount(acct)
	n, err := s.RestoreLedgerIdentities()
	if err != nil || n != 1 {
		t.Fatalf("restore count %d: %v", n, err)
	}
	if got := s.Store.AccountCopy(acct.ID).MachineID; got != rec.MachineID {
		t.Fatalf("unexpected identity %q", got)
	}
	n, err = s.RestoreLedgerIdentities()
	if err != nil || n != 0 {
		t.Fatalf("restore should be idempotent: %d %v", n, err)
	}
}
