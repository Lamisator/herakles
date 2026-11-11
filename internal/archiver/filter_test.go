package archiver

import (
	"runtime"
	"testing"
)

func TestFilter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	f, err := NewFilter([]string{"*.tmp", "**/node_modules", "/var/cache", "build/*", "# comment", ""})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"/home/a/x.tmp":                true,
		"/home/a/x.tmpl":               false,
		"/home/a/project/node_modules": true,
		"/node_modules":                true,
		"/var/cache":                   true,
		"/var/cache2":                  false,
		"/srv/var/cache":               false,
		"/home/a/build/out.o":          true,
		"/home/a/build":                false,
		"/home/a/src/main.go":          false,
	}
	for p, want := range cases {
		if got := f.Excluded(p); got != want {
			t.Errorf("%s: got %v want %v", p, got, want)
		}
	}
	if _, err := NewFilter([]string{"[unclosed"}); err == nil {
		t.Fatal("invalid pattern accepted")
	}
	var nilFilter *Filter
	if nilFilter.Excluded("/x") {
		t.Fatal("nil filter excludes")
	}
}

func TestSplitAbs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	got := splitAbs("/home//alice/./docs/")
	if len(got) != 3 || got[0] != "home" || got[2] != "docs" {
		t.Fatalf("%v", got)
	}
	if p := prefixPath("/home/alice/docs", got, 2); p != "/home/alice" {
		t.Fatal(p)
	}
}
