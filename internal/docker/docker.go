// Package docker discovers Docker Compose projects and standalone containers
// and knows which host data belongs to them: the project directory, bind
// mounts, named volumes and databases. It drives Docker through the docker
// CLI, which is present on every Docker host and handles Compose.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Compose labels set on every container of a Compose project.
const (
	LabelProject     = "com.docker.compose.project"
	LabelService     = "com.docker.compose.service"
	LabelWorkingDir  = "com.docker.compose.project.working_dir"
	LabelConfigFiles = "com.docker.compose.project.config_files"
)

// Client runs docker commands.
type Client struct {
	// Binary is the docker executable (default "docker").
	Binary string
}

// ErrUnavailable is returned when docker cannot be run.
var ErrUnavailable = errors.New("docker is not available")

func (c *Client) bin() string {
	if c != nil && c.Binary != "" {
		return c.Binary
	}
	return "docker"
}

// Run executes docker with args. stdout receives the output (discarded if
// nil); stderr is returned as part of the error.
func (c *Client) Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Stdin = stdin
	if stdout == nil {
		stdout = io.Discard
	}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("docker %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
		}
		return fmt.Errorf("docker %s: %s", strings.Join(args[:min(len(args), 2)], " "), msg)
	}
	return nil
}

func (c *Client) output(ctx context.Context, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := c.Run(ctx, nil, &out, args...)
	return out.Bytes(), err
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		q := p
		if len(q) > l.n {
			q = q[:l.n]
		}
		n, _ := l.w.Write(q)
		l.n -= n
	}
	return len(p), nil
}

// Mount is a container mount.
type Mount struct {
	Type        string `json:"type"` // bind, volume, tmpfs
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"` // host path
	Destination string `json:"destination"`
}

// Container is the part of `docker inspect` Herakles needs.
type Container struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Service string            `json:"service,omitempty"`
	Project string            `json:"project,omitempty"`
	Running bool              `json:"running"`
	State   string            `json:"state"`
	Env     []string          `json:"-"`
	Labels  map[string]string `json:"-"`
	Mounts  []Mount           `json:"mounts"`
}

// EnvValue returns the value of an environment variable of the container.
func (c Container) EnvValue(name string) string {
	for _, e := range c.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == name {
			return v
		}
	}
	return ""
}

// Project is a Compose project, a standalone container, or a project
// directory whose containers do not exist (yet).
type Project struct {
	Name        string      `json:"name"`
	WorkingDir  string      `json:"working_dir,omitempty"`
	ConfigFiles []string    `json:"config_files,omitempty"`
	Standalone  bool        `json:"standalone,omitempty"`
	Containers  []Container `json:"containers"`
	// Volumes created by Compose for the project (by label), including
	// those no container uses right now.
	Volumes []Volume `json:"volumes,omitempty"`
}

// Volume is a Docker volume.
type Volume struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
	Project    string `json:"project,omitempty"`
}

// Volumes lists all volumes with their Compose project label.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	out, err := c.output(ctx, "volume", "ls", "-q")
	if err != nil {
		return nil, err
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return nil, nil
	}
	out, err = c.output(ctx, append([]string{"volume", "inspect"}, names...)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name       string            `json:"Name"`
		Mountpoint string            `json:"Mountpoint"`
		Labels     map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker volume inspect: %w", err)
	}
	var vs []Volume
	for _, r := range raw {
		vs = append(vs, Volume{Name: r.Name, Mountpoint: r.Mountpoint, Project: r.Labels[LabelProject]})
	}
	return vs, nil
}

// Orphans returns volumes that belong to no project and are not mounted
// by any container: they are not part of any project backup.
func Orphans(ps []Project, vs []Volume) []Volume {
	used := map[string]bool{}
	for _, p := range ps {
		for _, c := range p.Containers {
			for _, m := range c.Mounts {
				if m.Type == "volume" {
					used[m.Name] = true
				}
			}
		}
		for _, v := range p.Volumes {
			used[v.Name] = true
		}
	}
	var out []Volume
	for _, v := range vs {
		if !used[v.Name] {
			out = append(out, v)
		}
	}
	return out
}

// Dirs returns where the project lives: its directory, or for a standalone
// container the sources of its bind mounts.
func (p Project) Dirs() []string {
	if !p.Standalone {
		return []string{p.WorkingDir}
	}
	var out []string
	for _, c := range p.Containers {
		for _, m := range c.Mounts {
			if m.Type == "bind" && m.Source != "" {
				out = append(out, m.Source)
			}
		}
	}
	return out
}

// Running reports how many containers are running.
func (p Project) Running() int {
	n := 0
	for _, c := range p.Containers {
		if c.Running {
			n++
		}
	}
	return n
}

type inspectJSON struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// Containers lists all containers (running and stopped).
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	out, err := c.output(ctx, "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = c.output(ctx, append([]string{"inspect", "--type", "container"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var raw []inspectJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var cs []Container
	for _, r := range raw {
		ct := Container{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image, Env: r.Config.Env,
			Labels: r.Config.Labels, Running: r.State.Running, State: r.State.Status,
			Project: r.Config.Labels[LabelProject], Service: r.Config.Labels[LabelService]}
		for _, m := range r.Mounts {
			ct.Mounts = append(ct.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination})
		}
		cs = append(cs, ct)
	}
	return cs, nil
}

// Group turns containers into projects: Compose projects by label, other
// containers as standalone projects named after the container.
func Group(cs []Container) []Project {
	byName := map[string]*Project{}
	var order []string
	for _, ct := range cs {
		name, standalone := ct.Project, false
		if name == "" {
			name, standalone = ct.Name, true
		}
		p := byName[name]
		if p == nil {
			p = &Project{Name: name, Standalone: standalone}
			byName[name] = p
			order = append(order, name)
		}
		if !standalone {
			if p.WorkingDir == "" {
				p.WorkingDir = ct.Labels[LabelWorkingDir]
			}
			if len(p.ConfigFiles) == 0 && ct.Labels[LabelConfigFiles] != "" {
				p.ConfigFiles = strings.Split(ct.Labels[LabelConfigFiles], ",")
			}
		}
		p.Containers = append(p.Containers, ct)
	}
	sort.Strings(order)
	out := make([]Project, 0, len(order))
	for _, n := range order {
		p := byName[n]
		sort.Slice(p.Containers, func(i, j int) bool { return p.Containers[i].Name < p.Containers[j].Name })
		out = append(out, *p)
	}
	return out
}

// composeFiles are the file names `docker compose` looks for.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// ProjectName normalises a directory name the way Compose does.
func ProjectName(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "-_")
}

// ScanDirs finds project directories (direct subdirectories of dirs that
// contain a compose file).
func ScanDirs(dirs []string) []Project { return ScanTree(dirs, 1) }

// ScanTree finds project directories up to depth levels below dirs. It does
// not descend into hidden directories or below a project directory.
func ScanTree(dirs []string, depth int) []Project {
	var out []Project
	var walk func(dir string, level int)
	walk = func(dir string, level int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if f := composeFile(sub); f != "" {
				out = append(out, Project{Name: ProjectName(sub), WorkingDir: sub, ConfigFiles: []string{f}})
				continue
			}
			if level < depth {
				walk(sub, level+1)
			}
		}
	}
	for _, d := range dirs {
		walk(d, 1)
	}
	return out
}

func composeFile(dir string) string {
	for _, f := range composeFiles {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil && st.Mode().IsRegular() {
			return filepath.Join(dir, f)
		}
	}
	return ""
}

// Search says where to look for project directories without containers:
// the subdirectories of Dirs and up to three levels below Trees.
type Search struct {
	Dirs  []string
	Trees []string
}

// Projects discovers all projects: those with containers plus project
// directories found by the search that have none, with their labelled
// volumes.
func (c *Client) Projects(ctx context.Context, search Search) ([]Project, error) {
	ps, _, err := c.ProjectsAndVolumes(ctx, search)
	return ps, err
}

// ProjectsAndVolumes is Projects plus the list of all volumes.
func (c *Client) ProjectsAndVolumes(ctx context.Context, search Search) ([]Project, []Volume, error) {
	cs, err := c.Containers(ctx)
	if err != nil {
		return nil, nil, err
	}
	vs, err := c.Volumes(ctx)
	if err != nil {
		return nil, nil, err
	}
	ps := Group(cs)
	known := map[string]bool{}
	for _, p := range ps {
		known[p.Name] = true
		if p.WorkingDir != "" {
			known["dir:"+filepath.Clean(p.WorkingDir)] = true
		}
	}
	// Directories inside a project that has containers belong to it (e.g. a
	// source checkout with its own compose file), not to a new project.
	var owned []string
	for _, p := range ps {
		if p.WorkingDir != "" {
			owned = append(owned, filepath.Clean(p.WorkingDir))
		}
	}
	inside := func(dir string) bool {
		for _, o := range owned {
			if dir != o && within(dir, o) {
				return true
			}
		}
		return false
	}
	for _, p := range append(ScanDirs(search.Dirs), ScanTree(search.Trees, 3)...) {
		if inside(filepath.Clean(p.WorkingDir)) {
			continue
		}
		if !known[p.Name] && !known["dir:"+filepath.Clean(p.WorkingDir)] {
			ps = append(ps, p)
			known[p.Name], known["dir:"+filepath.Clean(p.WorkingDir)] = true, true
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	for i := range ps {
		if ps[i].Standalone {
			continue
		}
		for _, v := range vs {
			if v.Project == ps[i].Name {
				ps[i].Volumes = append(ps[i].Volumes, v)
			}
		}
	}
	return ps, vs, nil
}

// Match reports whether name matches one of the glob patterns ("*" = all).
func Match(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Host paths that are never project data.
var systemPrefixes = []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/var/lib/docker/containers", "/var/lib/docker/overlay2"}
var systemFiles = map[string]bool{"/": true, "/etc/localtime": true, "/etc/timezone": true, "/etc/hostname": true,
	"/etc/hosts": true, "/etc/resolv.conf": true, "/etc/machine-id": true, "/etc/passwd": true, "/etc/group": true}

func systemPath(p string) bool {
	p = filepath.Clean(p)
	if systemFiles[p] {
		return true
	}
	for _, s := range systemPrefixes {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// DataOptions select what belongs to a project's backup.
type DataOptions struct {
	SkipVolumes    bool
	SkipBindMounts bool
	// Lstat is used to skip missing paths and sockets (tests replace it).
	Lstat func(string) (os.FileInfo, error)
}

// DataSet is the host data of a project.
type DataSet struct {
	// Paths to back up, without nested duplicates.
	Paths []string `json:"paths"`
	// Volumes maps named volumes to their host directories.
	Volumes map[string]string `json:"volumes,omitempty"`
	// Binds are bind-mounted host paths outside the project directory.
	Binds []string `json:"binds,omitempty"`
}

// Data returns the host paths holding the project's data.
func (p Project) Data(o DataOptions) DataSet {
	lstat := o.Lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	usable := func(pth string) bool {
		if pth == "" || systemPath(pth) {
			return false
		}
		st, err := lstat(pth)
		return err == nil && st.Mode()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) == 0
	}
	ds := DataSet{Volumes: map[string]string{}}
	var paths []string
	if p.WorkingDir != "" && usable(p.WorkingDir) {
		paths = append(paths, filepath.Clean(p.WorkingDir))
	}
	for _, ct := range p.Containers {
		for _, m := range ct.Mounts {
			switch m.Type {
			case "volume":
				if o.SkipVolumes || !usable(m.Source) {
					continue
				}
				ds.Volumes[m.Name] = filepath.Clean(m.Source)
				paths = append(paths, filepath.Clean(m.Source))
			case "bind":
				if o.SkipBindMounts || !usable(m.Source) {
					continue
				}
				src := filepath.Clean(m.Source)
				if p.WorkingDir == "" || !within(src, filepath.Clean(p.WorkingDir)) {
					ds.Binds = append(ds.Binds, src)
				}
				paths = append(paths, src)
			}
		}
	}
	if !o.SkipVolumes {
		for _, v := range p.Volumes {
			if _, ok := ds.Volumes[v.Name]; !ok && usable(v.Mountpoint) {
				ds.Volumes[v.Name] = filepath.Clean(v.Mountpoint)
				paths = append(paths, filepath.Clean(v.Mountpoint))
			}
		}
	}
	ds.Paths = topLevel(paths)
	ds.Binds = topLevel(ds.Binds)
	return ds
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// topLevel sorts, deduplicates and drops paths inside other paths.
func topLevel(ps []string) []string {
	sort.Strings(ps)
	var out []string
	for _, p := range ps {
		if len(out) > 0 && within(p, out[len(out)-1]) {
			continue
		}
		dup := false
		for _, o := range out {
			if within(p, o) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}

// Stop stops a project's containers.
func (c *Client) Stop(ctx context.Context, p Project) error {
	if p.Standalone {
		return c.Run(ctx, nil, nil, "stop", p.Name)
	}
	return c.Run(ctx, nil, nil, "compose", "-p", p.Name, "stop")
}

// Start starts the existing containers of a project.
func (c *Client) Start(ctx context.Context, p Project) error {
	if p.Standalone {
		return c.Run(ctx, nil, nil, "start", p.Name)
	}
	return c.Run(ctx, nil, nil, "compose", "-p", p.Name, "start")
}

// composeArgs addresses a project through its files and directory.
func composeArgs(p Project) ([]string, error) {
	if p.WorkingDir == "" || len(p.ConfigFiles) == 0 {
		return nil, fmt.Errorf("project %s: compose files unknown", p.Name)
	}
	args := []string{"compose", "-p", p.Name, "--project-directory", p.WorkingDir}
	for _, f := range p.ConfigFiles {
		args = append(args, "-f", f)
	}
	return args, nil
}

// Create creates containers, networks and volumes without starting them.
func (c *Client) Create(ctx context.Context, p Project) error {
	args, err := composeArgs(p)
	if err != nil {
		return err
	}
	return c.Run(ctx, nil, nil, append(args, "up", "--no-start")...)
}

// Up creates (if needed) and starts a project.
func (c *Client) Up(ctx context.Context, p Project) error {
	args, err := composeArgs(p)
	if err != nil {
		return err
	}
	return c.Run(ctx, nil, nil, append(args, "up", "-d")...)
}

// Inspect returns the raw `docker inspect` JSON of the containers.
func (c *Client) Inspect(ctx context.Context, names ...string) ([]byte, error) {
	if len(names) == 0 {
		return []byte("[]\n"), nil
	}
	return c.output(ctx, append([]string{"inspect", "--type", "container"}, names...)...)
}

// StartContainers starts containers by name.
func (c *Client) StartContainers(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	return c.Run(ctx, nil, nil, append([]string{"start"}, names...)...)
}
