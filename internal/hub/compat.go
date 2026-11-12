package hub

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Compatibility between a hub and its servers: every Herakles announces the
// capabilities it has in its Hello. The hub compares them with its own and
// explains what does not work together. Servers too old to announce
// capabilities get those of their version (Since).
//
// A new capability is added here when hub and servers depend on each other
// for it: a storage format they share, an API the hub's window calls on a
// server, or a change in the hub protocol.

// Capability is a feature that matters when a hub and a server work
// together.
type Capability struct {
	Name  string
	Since string // first version that has it
	// Severity when one side lacks it: "error" (something breaks or data
	// cannot be read) or "warning" (a page or function is unavailable).
	Severity string
	// ServerLacks and HubLacks explain the consequence when the server or
	// the hub lacks the capability ("" when it does not matter).
	ServerLacks, HubLacks string
}

// Capabilities lists what this version can do, oldest first.
var Capabilities = []Capability{
	{Name: "hub-protocol-1", Since: "0.9.0", Severity: "error",
		ServerLacks: "the server cannot talk to this hub",
		HubLacks:    "the hub cannot talk to this server"},
	{Name: "shared-repositories", Since: "0.10.0", Severity: "warning",
		ServerLacks: "repositories opened to all servers are not passed on to it"},
	{Name: "self-update", Since: "0.11.0", Severity: "warning",
		ServerLacks: "it cannot update itself: install the new version on it by hand once"},
	{Name: "image-browse", Since: "0.12.0", Severity: "warning",
		ServerLacks: "files inside its disk image backups cannot be browsed from the hub"},
	{Name: "storage-usage", Since: "0.12.0", Severity: "warning",
		ServerLacks: "the storage bars of its repositories stay empty"},
	{Name: "backup-files", Since: "0.13.0", Severity: "error",
		ServerLacks: "it cannot read backups stored as one file per backup: in repositories shared with newer servers it does not see their snapshots, and its own backups use the older format",
		HubLacks:    "the hub cannot read backups this server stores as one file per backup: it does not see them, and pruning shared repositories leaves them alone"},
	// Announcing capabilities: older servers get those of their version.
	{Name: "capabilities", Since: "0.13.0", Severity: "warning"},
}

// LocalCapabilities returns the names of this version's capabilities.
func LocalCapabilities() []string {
	out := make([]string, 0, len(Capabilities))
	for _, c := range Capabilities {
		out = append(out, c.Name)
	}
	return out
}

// CapabilitiesOf returns what a Hello announces, or for versions too old to
// announce anything, what their version had.
func CapabilitiesOf(h Hello) []string {
	if len(h.Capabilities) > 0 {
		return h.Capabilities
	}
	var out []string
	for _, c := range Capabilities {
		if versionAtLeast(h.Version, c.Since) {
			out = append(out, c.Name)
		}
	}
	return out
}

// Issue is something a hub and a server cannot do together.
type Issue struct {
	Capability string `json:"capability"`
	Severity   string `json:"severity"` // error, warning
	// Side is who lacks the capability: "server" or "hub".
	Side    string `json:"side"`
	Message string `json:"message"`
}

// Compatibility compares a server with the hub.
type Compatibility struct {
	// Status: "ok", "warning", "error", or "unknown" (never connected).
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities"`
	// Derived means the server did not announce its capabilities: they
	// were derived from its version.
	Derived bool    `json:"derived,omitempty"`
	Issues  []Issue `json:"issues,omitempty"`
	// Newer reports which side runs the newer version: "server", "hub" or "".
	Newer string `json:"newer,omitempty"`
}

// Compare checks a server's Hello against the hub's capabilities (hub
// names; the hub's version only decides which side to update).
func Compare(hubVersion string, hubCaps []string, server Hello) Compatibility {
	if server.Version == "" && len(server.Capabilities) == 0 {
		return Compatibility{Status: "unknown"}
	}
	c := Compatibility{Status: "ok", Capabilities: CapabilitiesOf(server), Derived: len(server.Capabilities) == 0}
	switch cmpVersion(server.Version, hubVersion) {
	case 1:
		c.Newer = "server"
	case -1:
		c.Newer = "hub"
	}
	has := func(list []string, name string) bool {
		for _, x := range list {
			if x == name {
				return true
			}
		}
		return false
	}
	known := map[string]Capability{}
	for _, k := range Capabilities {
		known[k.Name] = k
	}
	for _, k := range Capabilities {
		if has(hubCaps, k.Name) && !has(c.Capabilities, k.Name) && k.ServerLacks != "" {
			c.Issues = append(c.Issues, Issue{Capability: k.Name, Severity: k.Severity, Side: "server", Message: k.ServerLacks})
		}
		if !has(hubCaps, k.Name) && has(c.Capabilities, k.Name) && k.HubLacks != "" {
			c.Issues = append(c.Issues, Issue{Capability: k.Name, Severity: k.Severity, Side: "hub", Message: k.HubLacks})
		}
	}
	// Capabilities of a newer server this hub does not know at all.
	var unknown []string
	for _, n := range c.Capabilities {
		if _, ok := known[n]; !ok && !has(hubCaps, n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		c.Issues = append(c.Issues, Issue{Capability: strings.Join(unknown, ", "), Severity: "warning", Side: "hub",
			Message: fmt.Sprintf("the server can do things this hub does not know (%s); update the hub", strings.Join(unknown, ", "))})
	}
	for _, i := range c.Issues {
		if i.Severity == "error" {
			c.Status = "error"
			break
		}
		c.Status = "warning"
	}
	return c
}

// cmpVersion compares versions like "v0.12.3" or "0.13.0-4-gabc" by their
// numbers: -1, 0 or 1. Unparsable versions (dev builds) compare as equal.
func cmpVersion(a, b string) int {
	x, okx := versionNumbers(a)
	y, oky := versionNumbers(b)
	if !okx || !oky {
		return 0
	}
	for i := range 3 {
		switch {
		case x[i] < y[i]:
			return -1
		case x[i] > y[i]:
			return 1
		}
	}
	return 0
}

func versionAtLeast(v, min string) bool {
	if _, ok := versionNumbers(v); !ok {
		return false
	}
	return cmpVersion(v, min) >= 0
}

func versionNumbers(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
