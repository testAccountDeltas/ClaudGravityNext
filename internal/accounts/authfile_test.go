package accounts

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// D3: toggle, refresh-token rotation and project-id persistence used to
// read-modify-write the same auth file without a shared lock. Run with -race.
func TestAuthFile_ConcurrentWritersLoseNoField(t *testing.T) {
	freshSelector(t)
	const email = "d3@example.com"
	path := writeAuthFile(t, t.TempDir(), "d3.json",
		`{"email":"d3@example.com","refresh_token":"rt-0","project_id":"aicode-consumers","extra":"keep"}`)
	acc := &Record{Email: email, RefreshToken: "rt-0", FilePath: path, ProjectID: "aicode-consumers"}
	seedAccounts(t, acc)

	const rounds = 50
	var wg sync.WaitGroup
	for i := 1; i <= rounds; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			_ = globalAccountManager.ToggleAccountDisabled(email, i%2 == 0)
		}(i)
		go func(i int) {
			defer wg.Done()
			rt := fmt.Sprintf("rt-%d", i)
			updateAuthFile(path, setRefresh(rt))
		}(i)
		go func(i int) {
			defer wg.Done()
			persistAccountFields(path, fmt.Sprintf("proj-%d", i), true)
		}(i)
	}
	wg.Wait()

	// Final deterministic writes after the storm: each writer once more, in
	// sequence, then every field must be present at its last value.
	_ = globalAccountManager.ToggleAccountDisabled(email, true)
	updateAuthFile(path, setRefresh("rt-final"))
	persistAccountFields(path, "proj-final", true)

	b := []byte(readFile(t, path))
	if !gjson.ValidBytes(b) {
		t.Fatalf("file corrupted: %s", b)
	}
	for k, want := range map[string]string{"extra": "keep", "email": email, "refresh_token": "rt-final", "project_id": "proj-final"} {
		if got := gjson.GetBytes(b, k).String(); got != want {
			t.Errorf("%s = %q want %q", k, got, want)
		}
	}
	if !gjson.GetBytes(b, "disabled").Bool() || !gjson.GetBytes(b, "project_id_probed").Bool() {
		t.Errorf("flags lost: %s", b)
	}
}

// Every writer owns a distinct key: a lost update (stale read overwriting a
// concurrent write) makes at least one of the 150 keys vanish.
func TestAuthFile_NoLostUpdateAcrossDistinctKeys(t *testing.T) {
	path := writeAuthFile(t, t.TempDir(), "k.json", `{"email":"k@example.com"}`)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func(i int) { defer wg.Done(); updateAuthFile(path, setKey(fmt.Sprint("dis", i), true)) }(i)
		go func(i int) { defer wg.Done(); updateAuthFile(path, setKey(fmt.Sprint("rt", i), "x")) }(i)
		go func(i int) { defer wg.Done(); updateAuthFile(path, setKey(fmt.Sprint("pid", i), i)) }(i)
	}
	wg.Wait()
	b := []byte(readFile(t, path))
	for i := 0; i < n; i++ {
		for _, p := range []string{"dis", "rt", "pid"} {
			if !gjson.GetBytes(b, fmt.Sprint(p, i)).Exists() {
				t.Fatalf("lost update: key %s%d missing", p, i)
			}
		}
	}
}

func TestUpdateAuthFile_MissingFileAndEmptyPathAreNoOps(t *testing.T) {
	updateAuthFile("", setRefresh("x"))
	p := t.TempDir() + "/nope.json"
	updateAuthFile(p, setRefresh("x"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("must not create a file that did not exist")
	}
}
