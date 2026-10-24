package accounts

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func loadFrom(t *testing.T, dir string) *AccountManager {
	t.Helper()
	am := &AccountManager{accounts: map[string]*Record{}, authDir: dir}
	if err := am.LoadAll(); err != nil {
		t.Fatal(err)
	}
	return am
}

func emails(am *AccountManager) []string {
	var out []string
	for _, a := range am.GetAccounts() {
		out = append(out, a.Email)
	}
	sort.Strings(out)
	return out
}

func TestLoadAll_OnlyValidAccountFilesLoad(t *testing.T) {
	dir := t.TempDir()
	writeAuthFile(t, dir, "good.json", `{"email":"good@example.com","refresh_token":"r","access_token":"a","project_id":"proj","project_id_probed":true,"disabled":true}`)
	writeAuthFile(t, dir, "truncated.json", `{"email":"trunc@example.com","refresh_tok`)
	writeAuthFile(t, dir, "garbage.json", `not json at all`)
	writeAuthFile(t, dir, "good.json.123.tmp", `{"email":"tmp@example.com"}`)
	writeAuthFile(t, dir, ".hidden.json", `{"email":"hidden@example.com"}`)
	writeAuthFile(t, dir, "noemail.json", `{"refresh_token":"r"}`)
	writeAuthFile(t, dir, "readme.txt", `{"email":"txt@example.com"}`)
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	am := loadFrom(t, dir)
	got := emails(am)
	if len(got) != 1 || got[0] != "good@example.com" {
		t.Fatalf("want only good@example.com, got %v", got)
	}
	a, _ := am.GetAccount("good@example.com")
	if a.RefreshToken != "r" || a.AccessToken != "a" || a.ProjectID != "proj" || !a.ProjectIDProbed || !a.Disabled {
		t.Fatalf("fields not loaded: %+v", a)
	}
	if a.FilePath != filepath.Join(dir, "good.json") {
		t.Fatalf("FilePath = %q", a.FilePath)
	}
}

func TestLoadAll_MissingProjectIDUsesFallback(t *testing.T) {
	dir := t.TempDir()
	writeAuthFile(t, dir, "a.json", `{"email":"a@example.com"}`)
	a, _ := loadFrom(t, dir).GetAccount("a@example.com")
	if a.ProjectID != fallbackProjectID || a.ProjectIDProbed {
		t.Fatalf("got %+v", a)
	}
}

func TestLoadAll_ExpiresAtLoadedWhenPresent(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	writeAuthFile(t, dir, "a.json", `{"email":"a@example.com","expires_at":"`+exp.Format(time.RFC3339)+`"}`)
	writeAuthFile(t, dir, "b.json", `{"email":"b@example.com","expires_at":"yesterday-ish"}`)
	am := loadFrom(t, dir)
	if a, _ := am.GetAccount("a@example.com"); !a.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v", a.ExpiresAt)
	}
	if b, _ := am.GetAccount("b@example.com"); !b.ExpiresAt.IsZero() {
		t.Fatalf("unparseable expiry must stay zero, got %v", b.ExpiresAt)
	}
}

func TestLoadAll_ReloadRemovesDeletedAccountsAndKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	pa := writeAuthFile(t, dir, "a.json", `{"email":"a@example.com"}`)
	writeAuthFile(t, dir, "b.json", `{"email":"b@example.com"}`)
	am := loadFrom(t, dir)
	if len(emails(am)) != 2 {
		t.Fatalf("got %v", emails(am))
	}
	if err := os.Remove(pa); err != nil {
		t.Fatal(err)
	}
	writeAuthFile(t, dir, "c.json", `{"email":"c@example.com"}`)
	if err := am.LoadAll(); err != nil {
		t.Fatal(err)
	}
	got := emails(am)
	if len(got) != 2 || got[0] != "b@example.com" || got[1] != "c@example.com" {
		t.Fatalf("reload must drop a and add c, got %v", got)
	}
}

func TestLoadAll_CreatesMissingAuthDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "auths")
	am := loadFrom(t, dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || len(emails(am)) != 0 {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestLoadAll_NestedDirectoryAccountLoads(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "team")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAuthFile(t, sub, "n.json", `{"email":"n@example.com"}`)
	if got := emails(loadFrom(t, dir)); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}
