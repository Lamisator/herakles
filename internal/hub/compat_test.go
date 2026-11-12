package hub

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	caps := LocalCapabilities()
	// Same capabilities: compatible.
	if c := Compare("v0.13.0", caps, Hello{Version: "v0.13.0", Capabilities: caps}); c.Status != "ok" || len(c.Issues) != 0 || c.Derived {
		t.Fatalf("same: %+v", c)
	}
	// Never connected.
	if c := Compare("v0.13.0", caps, Hello{}); c.Status != "unknown" {
		t.Fatalf("unknown: %+v", c)
	}
	// An old server announces nothing: derived from its version. It lacks
	// the backup files (error) and image browsing (warning); update it.
	c := Compare("v0.13.0", caps, Hello{Version: "v0.11.0"})
	if c.Status != "error" || !c.Derived || c.Newer != "hub" {
		t.Fatalf("old server: %+v", c)
	}
	got := map[string]Issue{}
	for _, i := range c.Issues {
		got[i.Capability] = i
	}
	if got["backup-files"].Severity != "error" || got["backup-files"].Side != "server" || got["image-browse"].Severity != "warning" {
		t.Fatalf("old server issues: %+v", c.Issues)
	}
	if _, ok := got["self-update"]; ok {
		t.Fatal("0.11 can update itself")
	}
	// A v0.12 server only lacks the backup files.
	if c := Compare("v0.13.0", caps, Hello{Version: "v0.12.3"}); len(c.Issues) != 1 || c.Issues[0].Capability != "backup-files" {
		t.Fatalf("0.12: %+v", c)
	}
	// A newer server with a capability this hub lacks or does not know.
	old := []string{"hub-protocol-1", "shared-repositories", "self-update", "image-browse", "storage-usage"}
	c = Compare("v0.12.3", old, Hello{Version: "v0.14.0", Capabilities: append(caps, "teleport")})
	if c.Status != "error" || c.Newer != "server" {
		t.Fatalf("newer server: %+v", c)
	}
	var msgs []string
	for _, i := range c.Issues {
		if i.Side != "hub" {
			t.Fatalf("the hub lacks it: %+v", i)
		}
		msgs = append(msgs, i.Message)
	}
	if all := strings.Join(msgs, "|"); !strings.Contains(all, "teleport") || !strings.Contains(all, "one file per backup") {
		t.Fatalf("messages: %s", all)
	}
}

func TestVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.12.3", "v0.13.0", -1}, {"0.13.0", "v0.13.0", 0}, {"v0.13.0-4-gabc-dirty", "v0.12.9", 1},
		{"v1.0.0", "v0.99.99", 1}, {"dev", "v0.13.0", 0},
	}
	for _, c := range cases {
		if got := cmpVersion(c.a, c.b); got != c.want {
			t.Errorf("cmp(%s, %s) = %d", c.a, c.b, got)
		}
	}
	if versionAtLeast("dev", "0.9.0") || !versionAtLeast("v0.9.0", "0.9.0") {
		t.Fatal("versionAtLeast")
	}
}
