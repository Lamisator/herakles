package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v0.11.0", "v0.10.0", true},
		{"v0.10.1", "v0.10.0", true},
		{"v1.0.0", "v0.99.9", true},
		{"v0.10.0", "v0.10.0", false},
		{"v0.9.9", "v0.10.0", false},
		{"v0.10.0", "v0.10.0-3-gabcdef", false},
		{"v0.10.1", "v0.10.0-3-gabcdef", true},
		{"v0.10.1", "dev", false},
		{"v0.10.1", "v0.10.0-dirty", true},
		{"garbage", "v0.1.0", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

// release serves a signed latest.json and one file.
type release struct {
	srv  *httptest.Server
	key  ed25519.PrivateKey
	data []byte
	sum  string
	json []byte
	sig  string
}

func newRelease(t *testing.T, version string) *release {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	old := releaseKey
	releaseKey = pub
	t.Cleanup(func() { releaseKey = old })
	r := &release{key: key, data: []byte("#!/bin/sh\necho new herakles\n")}
	h := sha256.Sum256(r.data)
	r.sum = hex.EncodeToString(h[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.json) })
	mux.HandleFunc("/latest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(r.sig + "\n")) })
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.data) })
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	r.publish(version, r.sum)
	return r
}

func (r *release) publish(version, sum string) {
	f := File{URL: r.srv.URL + "/file", SHA256: sum, Size: int64(len(r.data))}
	r.json, _ = json.Marshal(Release{Version: version, Files: map[string]File{platformFile(runtime.GOOS, runtime.GOARCH): f, "windows-setup.exe": f}})
	r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.key, r.json))
}

func TestCheckVerifiesSignature(t *testing.T) {
	r := newRelease(t, "v0.11.0")
	url := r.srv.URL + "/latest.json"
	rel, err := Check(context.Background(), url)
	if err != nil || rel.Version != "v0.11.0" {
		t.Fatal(rel, err)
	}
	// Changed after signing.
	r.json = []byte(strings.Replace(string(r.json), "v0.11.0", "v9.0.0", 1))
	if _, err := Check(context.Background(), url); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatal("a tampered announcement must be refused:", err)
	}
	// Signed with another key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(other, r.json))
	if _, err := Check(context.Background(), url); err == nil {
		t.Fatal("a foreign signature must be refused")
	}
}

func TestInstallFile(t *testing.T) {
	r := newRelease(t, "v0.11.0")
	rel, err := Check(context.Background(), r.srv.URL+"/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "herakles")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Method{Kind: "file", Exe: exe}
	if err := Install(context.Background(), rel, m); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(r.data) {
		t.Fatalf("not replaced: %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o100 == 0 && runtime.GOOS != "windows" {
		t.Fatal("not executable")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatal("left files behind:", ents)
	}

	// A checksum that does not match leaves the program alone.
	os.WriteFile(exe, []byte("old"), 0o755)
	r.publish("v0.11.0", strings.Repeat("0", 64))
	rel, _ = Check(context.Background(), r.srv.URL+"/latest.json")
	if err := Install(context.Background(), rel, m); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("bad checksum accepted:", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("program changed after a failed download")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatal("left files behind:", ents)
	}
}

func TestCheckerTellsOnce(t *testing.T) {
	r := newRelease(t, "v0.11.0")
	told := 0
	c := &Checker{URL: r.srv.URL + "/latest.json", Current: "v0.10.0", OnNew: func(*Release) { told++ }}
	for i := 0; i < 3; i++ {
		if rel, err := c.CheckNow(context.Background()); err != nil || rel == nil {
			t.Fatal(rel, err)
		}
	}
	if told != 1 {
		t.Fatal("OnNew calls:", told)
	}
	st := c.State(Method{Kind: "file"})
	if !st.Available || st.Latest != "v0.11.0" || st.Current != "v0.10.0" {
		t.Fatalf("%+v", st)
	}
	same := &Checker{URL: c.URL, Current: "v0.11.0", OnNew: func(*Release) { t.Fatal("no update") }}
	if rel, err := same.CheckNow(context.Background()); err != nil || rel != nil || same.Available() != nil {
		t.Fatal(rel, err)
	}
}
