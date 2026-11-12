package archiver

import (
	"runtime"
	"testing"

	"github.com/lamisator/herakles/internal/fsutil"
	tu "github.com/lamisator/herakles/internal/testutil"
)

// A backup of / lists "/" as its path and turns the top-level entries into
// sources; mount points of kernel file systems stay empty directories.
func TestRootSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no single root")
	}
	repo, _ := tu.NewRepo(t)
	a, err := New(repo, Options{Paths: []string{"/"}, Excludes: []string{"/tmp"},
		Streams: []Stream{{Path: "/herakles-databases/pg/app.sql"}}})
	if err != nil {
		t.Fatal(err)
	}
	root, paths, err := a.buildVTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/" {
		t.Fatalf("paths %v", paths)
	}
	if etc := root.children["etc"]; etc == nil || !etc.source || etc.realPath != "/etc" {
		t.Fatalf("etc: %+v", etc)
	}
	if root.children["tmp"] != nil {
		t.Fatal("excluded /tmp was added")
	}
	if p := root.children["proc"]; fsutil.PseudoFS("/proc") && (p == nil || p.source || len(p.children) != 0) {
		t.Fatalf("/proc must be an empty directory: %+v", p)
	}
	if db := root.children["herakles-databases"]; db == nil || db.children["pg"].children["app.sql"].stream == nil {
		t.Fatal("database dump next to the root entries is missing")
	}
}
