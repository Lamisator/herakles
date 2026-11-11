package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/repository"
)

// ProjectResult is the outcome of backing up one Docker project.
type ProjectResult struct {
	Project   string              `json:"project"`
	Status    string              `json:"status"` // success, warning, failure
	Error     string              `json:"error,omitempty"`
	Snapshot  string              `json:"snapshot,omitempty"`
	Type      string              `json:"type,omitempty"`
	Summary   *repository.Summary `json:"summary,omitempty"`
	Dumps     int                 `json:"dumps,omitempty"`
	Stopped   bool                `json:"stopped,omitempty"`
	Forgotten int                 `json:"forgotten,omitempty"`
}

// Reasons for skipping a database dump that are not failures.
const (
	skipNotRunning = "container not running"
	skipNoServer   = "no database server answering in this container"
)

// DockerClient returns the docker client for a job.
func DockerClient(job *config.Job) *docker.Client {
	if job.Docker != nil {
		return &docker.Client{Binary: job.Docker.Binary}
	}
	return &docker.Client{}
}

// DockerProjects lists the projects a docker job backs up.
func (e *Engine) DockerProjects(ctx context.Context, job *config.Job) ([]docker.Project, error) {
	_, sel, err := e.dockerProjects(ctx, job)
	return sel, err
}

// DockerProjectsAll returns all projects found for the job and the selected
// ones; data sets must be computed over all of them (docker.DataSets).
func (e *Engine) DockerProjectsAll(ctx context.Context, job *config.Job) (all, selected []docker.Project, err error) {
	return e.dockerProjects(ctx, job)
}

// DockerDataOptions are the data options of a docker job.
func DockerDataOptions(job *config.Job) docker.DataOptions {
	if job.Docker == nil {
		return docker.DataOptions{}
	}
	return docker.DataOptions{SkipVolumes: job.Docker.SkipVolumes, SkipBindMounts: job.Docker.SkipBindMounts}
}

// dockerProjects returns all projects found for the job and the selected ones.
func (e *Engine) dockerProjects(ctx context.Context, job *config.Job) (all, out []docker.Project, err error) {
	if job.Docker == nil {
		return nil, nil, fmt.Errorf("job %s has no docker section", job.Name)
	}
	all, err = DockerClient(job).Projects(ctx, DockerSearch(job))
	if err != nil {
		return nil, nil, err
	}
	for _, p := range all {
		if p.Standalone && job.Docker.SkipStandalone {
			continue
		}
		if job.Docker.Selects(p.Name, p.Dirs()...) {
			out = append(out, p)
		}
	}
	return all, out, nil
}

// Remainder returns what the job's own snapshot covers besides its paths:
// the docker folders minus the data of every project found in them, so
// that nothing in the folders is missed and nothing is stored twice.
func Remainder(job *config.Job, all []docker.Project) (paths, excludes []string) {
	if job.Docker == nil {
		return nil, nil
	}
	for _, f := range job.Docker.Folders {
		if _, err := os.Stat(f); err == nil {
			paths = append(paths, filepath.Clean(f))
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	for _, ds := range docker.DataSets(all, DockerDataOptions(job)) {
		for _, d := range ds.Paths {
			if job.Docker.InFolders(d) != "" {
				excludes = append(excludes, globEscape(d))
			}
		}
	}
	return paths, excludes
}

// globEscape makes a path safe to use as an exclude pattern.
func globEscape(p string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "{", "\\{").Replace(p)
}

// DockerSearch says where a job looks for projects without containers.
func DockerSearch(job *config.Job) docker.Search {
	dirs, trees := job.Docker.SearchDirs()
	return docker.Search{Dirs: dirs, Trees: trees}
}

// DockerMetaDir is where a project's dumps and inspect output are staged.
func (e *Engine) DockerMetaDir(job, project string) string {
	return filepath.Join(e.Config().GetStateDir(), "docker", job, project)
}

// runDockerBackup creates one snapshot per selected project ("job/project")
// plus one of the job's own paths, if any.
func (e *Engine) runDockerBackup(ctx context.Context, job *config.Job, rc *config.Repository, ro RunOptions, res *JobResult, log *slog.Logger) error {
	all, projects, err := e.dockerProjects(ctx, job)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	if len(ro.Projects) > 0 {
		var sel []docker.Project
		for _, name := range ro.Projects {
			i := slices.IndexFunc(projects, func(p docker.Project) bool { return p.Name == name })
			if i < 0 {
				return fmt.Errorf("job %s: no Docker project %q (see 'herakles docker ls')", job.Name, name)
			}
			sel = append(sel, projects[i])
		}
		projects = sel
	}
	repo, err := e.Open(ctx, rc)
	if err != nil {
		return err
	}
	defer repo.Close()

	sum := &repository.Summary{}
	add := func(s *repository.Summary) {
		if s == nil {
			return
		}
		sum.FilesNew += s.FilesNew
		sum.FilesChanged += s.FilesChanged
		sum.FilesUnmodified += s.FilesUnmodified
		sum.TotalFiles += s.TotalFiles
		sum.TotalBytes += s.TotalBytes
		sum.BytesAdded += s.BytesAdded
		sum.BytesStored += s.BytesStored
		sum.Errors += s.Errors
	}
	forgotten := 0
	var failed, warned []string
	sets := docker.DataSets(all, DockerDataOptions(job))
	for _, p := range projects {
		pr := e.backupProject(ctx, repo, job, p, sets[p.Name], ro, log.With("project", p.Name))
		res.Projects = append(res.Projects, pr)
		add(pr.Summary)
		forgotten += pr.Forgotten
		switch pr.Status {
		case "failure":
			failed = append(failed, p.Name)
		case "warning":
			warned = append(warned, p.Name)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// The job's own snapshot: its paths plus whatever in the docker
	// folders belongs to no project.
	rest, restExcl := Remainder(job, all)
	if len(ro.Projects) == 0 && (len(job.Paths) > 0 || len(job.Devices) > 0 || len(rest) > 0) {
		var paths []string
		for _, p := range job.Paths {
			paths = append(paths, config.ExpandHome(p))
		}
		u, err := e.backupUnit(ctx, repo, job, unit{Name: job.Name, Paths: append(paths, rest...), Devices: job.Devices, Excludes: restExcl}, ro, log)
		if err != nil {
			return fmt.Errorf("paths: %w", err)
		}
		res.Snapshot = u.snap.ID().String()
		add(u.snap.Summary)
		forgotten += u.forgotten
	}
	res.Summary, res.Forgotten = sum, forgotten
	if res.Type == "" && len(res.Projects) > 0 {
		res.Type = res.Projects[0].Type
	}
	if forgotten > 0 && job.Prune && !ro.DryRunRetention {
		st, err := e.Prune(ctx, repo, rc.Maintenance.RepackThreshold, false, nil)
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		res.Pruned = &st
	}
	if len(failed) > 0 && len(failed) == len(projects) {
		return fmt.Errorf("all Docker projects failed: %s", strings.Join(failed, ", "))
	}
	var msgs []string
	if len(failed) > 0 {
		msgs = append(msgs, "failed projects: "+strings.Join(failed, ", "))
	}
	if len(warned) > 0 {
		msgs = append(msgs, "projects with warnings: "+strings.Join(warned, ", "))
	}
	res.Error = strings.Join(msgs, "; ")
	return nil
}

// backupProject dumps the project's databases (or stops it), then saves its
// directory, bind mounts, volumes and the dumps as one snapshot.
func (e *Engine) backupProject(ctx context.Context, repo *repository.Repository, job *config.Job, p docker.Project, ds docker.DataSet, ro RunOptions, log *slog.Logger) ProjectResult {
	pr := ProjectResult{Project: p.Name, Status: "success"}
	fail := func(err error) ProjectResult {
		pr.Status, pr.Error = "failure", err.Error()
		log.Error("project backup failed", "err", err)
		return pr
	}
	dc := DockerClient(job)
	opts := job.Docker
	for _, sh := range ds.Shared {
		log.Info("bind mount contains other projects; they are backed up on their own", "path", sh)
	}
	info := &repository.DockerInfo{Project: p.Name, Standalone: p.Standalone, WorkingDir: p.WorkingDir,
		ConfigFiles: p.ConfigFiles, Volumes: ds.Volumes, Binds: ds.Binds}
	var running []string
	for _, ct := range p.Containers {
		info.Containers = append(info.Containers, repository.DockerContainer{Name: ct.Name, Service: ct.Service, Image: ct.Image, Running: ct.Running})
		if ct.Running {
			running = append(running, ct.Name)
		}
	}
	if len(info.Volumes) == 0 {
		info.Volumes = nil
	}

	meta := e.DockerMetaDir(job.Name, p.Name)
	if err := os.RemoveAll(meta); err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(meta, 0o700); err != nil {
		return fail(err)
	}
	info.MetaDir = meta
	if raw, err := dc.Inspect(ctx, names(p.Containers)...); err == nil {
		_ = os.WriteFile(filepath.Join(meta, "inspect.json"), raw, 0o600)
	} else {
		log.Warn("docker inspect failed", "err", err)
	}

	stop := opts.Stops(p.Name) && len(running) > 0
	if stop {
		log.Info("stopping project for a consistent backup", "containers", len(running))
		if err := dc.Stop(ctx, p); err != nil {
			_ = dc.StartContainers(context.WithoutCancel(ctx), running...)
			return fail(fmt.Errorf("stop: %w", err))
		}
		info.Stopped, pr.Stopped = true, true
		defer func() {
			if err := dc.StartContainers(context.WithoutCancel(ctx), running...); err != nil {
				log.Error("could not restart project", "err", err)
				pr.Status, pr.Error = "failure", "restart after backup: "+err.Error()
			}
		}()
	} else if !opts.SkipDumps {
		for _, ct := range p.Containers {
			kind, ok := docker.DetectDB(ct)
			if !ok {
				continue
			}
			d := repository.DockerDump{Container: ct.Name, Service: ct.Service, Kind: string(kind), DataDir: docker.DataDir(ct, kind)}
			if !ct.Running {
				d.Error = skipNotRunning
			} else if dc.Ready(ctx, ct, kind) != nil {
				d.Error = skipNoServer
			} else if err := dumpTo(ctx, dc, ct, kind, filepath.Join(meta, ct.Name+"."+kind.Extension())); err != nil {
				d.Error = err.Error()
			} else {
				d.File = ct.Name + "." + kind.Extension()
				pr.Dumps++
			}
			switch d.Error {
			case "":
			case skipNotRunning, skipNoServer:
				log.Info("database dump skipped", "container", ct.Name, "reason", d.Error)
			default:
				log.Warn("database dump failed", "container", ct.Name, "err", d.Error)
			}
			info.Dumps = append(info.Dumps, d)
		}
	}
	if b, err := json.MarshalIndent(info, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(meta, "herakles-project.json"), b, 0o600)
	}

	u, err := e.backupUnit(ctx, repo, job, unit{Name: job.Name + "/" + p.Name, Paths: append(ds.Paths, meta), Docker: info}, ro, log)
	if err != nil {
		return fail(err)
	}
	pr.Snapshot, pr.Type, pr.Summary, pr.Forgotten = u.snap.ID().String(), u.typ, u.snap.Summary, u.forgotten
	if u.snap.Summary != nil && u.snap.Summary.Errors > 0 {
		pr.Status = "warning"
	}
	for _, d := range info.Dumps {
		if d.Error != "" && d.Error != skipNotRunning && d.Error != skipNoServer {
			pr.Status = "warning"
			pr.Error = "dump of " + d.Container + " failed: " + d.Error
		}
	}
	log.Info("project backed up", "snapshot", pr.Snapshot, "dumps", pr.Dumps, "stopped", pr.Stopped)
	return pr
}

func names(cs []docker.Container) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// dumpTo writes a database dump atomically.
func dumpTo(ctx context.Context, dc *docker.Client, ct docker.Container, kind docker.DBKind, file string) error {
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = dc.Dump(ctx, ct, kind, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if st, serr := os.Stat(tmp); serr == nil && st.Size() == 0 {
			err = fmt.Errorf("empty dump")
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}
