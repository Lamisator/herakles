// Package dockertest provides a fake docker CLI for tests. It serves
// prepared containers, answers database dumps with recognisable text and
// records every call.
package dockertest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/docker"
)

const script = `#!/bin/sh
D="$(dirname "$0")"
echo "$*" >> "$D/log"
case "$1" in
ps) cat "$D/ids" ;;
inspect) cat "$D/inspect.json" ;;
exec)
	shift
	[ "$1" = "-i" ] && shift
	name=$1
	shift
	[ -f "$D/fail-$name" ] && { echo "simulated failure" >&2; exit 1; }
	case "$1" in
	pg_dumpall) [ -f "$D/fail-dump-$name" ] && { echo "dump failed" >&2; exit 1; }; echo "-- postgres dump of $name" ;;
	pg_isready) exit 0 ;;
	psql) cat > "$D/loaded-$name" ;;
	sh)
		case "$3" in
		*mongodump*) echo "mongo archive of $name" ;;
		*mongorestore*) cat > "$D/loaded-$name" ;;
		*adminCommand*) exit 0 ;;
		*all-databases*) echo "-- mysql dump of $name" ;;
		*ping*) exit 0 ;;
		*) cat > "$D/loaded-$name" ;;
		esac ;;
	esac ;;
esac
exit 0
`

// Container describes a fake container.
type Container struct {
	Name        string
	Image       string
	Project     string // compose project ("" = standalone)
	Service     string
	WorkingDir  string
	ConfigFiles []string
	Running     bool
	Env         []string
	Mounts      []docker.Mount
}

// Fake is a fake docker binary in a temp directory.
type Fake struct {
	Dir string
	Bin string
	t   testing.TB
}

// New creates the fake; it skips the test on Windows.
func New(t testing.TB) *Fake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker CLI is a shell script")
	}
	dir := t.TempDir()
	f := &Fake{Dir: dir, Bin: filepath.Join(dir, "docker"), t: t}
	if err := os.WriteFile(f.Bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Set()
	return f
}

// Set replaces the containers.
func (f *Fake) Set(cs ...Container) {
	f.t.Helper()
	var ids []string
	var out []map[string]any
	for i, c := range cs {
		id := strings.Repeat(string(rune('a'+i%26)), 12)
		ids = append(ids, id)
		labels := map[string]string{}
		if c.Project != "" {
			labels[docker.LabelProject] = c.Project
			labels[docker.LabelService] = c.Service
			labels[docker.LabelWorkingDir] = c.WorkingDir
			labels[docker.LabelConfigFiles] = strings.Join(c.ConfigFiles, ",")
		}
		var mounts []map[string]string
		for _, m := range c.Mounts {
			mounts = append(mounts, map[string]string{"Type": m.Type, "Name": m.Name, "Source": m.Source, "Destination": m.Destination})
		}
		status := "exited"
		if c.Running {
			status = "running"
		}
		out = append(out, map[string]any{
			"Id": id, "Name": "/" + c.Name,
			"Config": map[string]any{"Image": c.Image, "Env": c.Env, "Labels": labels},
			"State":  map[string]any{"Status": status, "Running": c.Running},
			"Mounts": mounts,
		})
	}
	b, _ := json.Marshal(out)
	if err := os.WriteFile(filepath.Join(f.Dir, "inspect.json"), b, 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, "ids"), []byte(strings.Join(ids, "\n")+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// Calls returns the recorded docker invocations.
func (f *Fake) Calls() []string {
	b, _ := os.ReadFile(filepath.Join(f.Dir, "log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Called reports whether a call with this prefix was made.
func (f *Fake) Called(prefix string) bool {
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// ResetCalls clears the call log.
func (f *Fake) ResetCalls() { _ = os.Remove(filepath.Join(f.Dir, "log")) }

// Loaded returns what was loaded into a container's database.
func (f *Fake) Loaded(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.Dir, "loaded-"+name))
	return string(b)
}

// Fail makes every exec in the container fail.
func (f *Fake) Fail(name string) {
	_ = os.WriteFile(filepath.Join(f.Dir, "fail-"+name), nil, 0o644)
}

// FailDump makes only database dumps in the container fail.
func (f *Fake) FailDump(name string) {
	_ = os.WriteFile(filepath.Join(f.Dir, "fail-dump-"+name), nil, 0o644)
}
