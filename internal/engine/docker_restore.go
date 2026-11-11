package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
)

// Database restore modes for a Docker project.
const (
	DBAuto  = "auto"  // files for stopped-mode snapshots, otherwise dumps
	DBDumps = "dumps" // fresh database directories, then load the dumps
	DBFiles = "files" // restore the raw database files
)

// ProjectRestoreOptions configure RestoreProject.
type ProjectRestoreOptions struct {
	// Snapshot is "latest" (default) or a snapshot ID prefix.
	Snapshot string
	// Databases is auto (default), dumps or files.
	Databases string
	// NoStart leaves the project stopped (only with file restores).
	NoStart bool
	// Target restores the files below this directory instead of their
	// original places; Docker is not touched.
	Target string
	// KeepOld keeps the replaced data (<path>.herakles-old-<time>).
	KeepOld bool
	// DryRun reports what would happen.
	DryRun bool
	// Progress receives human readable steps.
	Progress func(msg string)
}

// ProjectRestoreResult reports what RestoreProject did.
type ProjectRestoreResult struct {
	Project   string   `json:"project"`
	Snapshot  string   `json:"snapshot"`
	Time      string   `json:"time"`
	Databases string   `json:"databases"`
	Paths     []string `json:"paths"`
	Loaded    []string `json:"loaded,omitempty"`     // databases loaded from dumps
	OldCopies []string `json:"old_copies,omitempty"` // kept replaced data
	Warnings  []string `json:"warnings,omitempty"`
	Started   bool     `json:"started"`
}

// ProjectSnapshots lists the snapshots of one project, newest first.
func (e *Engine) ProjectSnapshots(ctx context.Context, unitName string) ([]*repository.Snapshot, error) {
	job, rc, err := e.dockerJob(unitName)
	if err != nil {
		return nil, err
	}
	repo, err := e.Open(ctx, rc)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	all, err := repo.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := repository.FilterSnapshots(all, repository.SnapshotFilter{Job: unitName, Hostname: jobHost(job)})
	slices.SortFunc(out, func(a, b *repository.Snapshot) int { return b.Time.Compare(a.Time) })
	return out, nil
}

func jobHost(job *config.Job) string {
	if job.Hostname != "" {
		return job.Hostname
	}
	h, _ := os.Hostname()
	return h
}

func (e *Engine) dockerJob(unitName string) (*config.Job, *config.Repository, error) {
	jobName, project := config.SplitUnit(unitName)
	job := e.Config().Job(jobName)
	if job == nil {
		return nil, nil, fmt.Errorf("unknown job %q", jobName)
	}
	if job.Docker == nil || project == "" {
		return nil, nil, fmt.Errorf("%q is not a Docker project of a docker job (use JOB/PROJECT)", unitName)
	}
	rc := e.Config().Repo(job.Repository)
	if rc == nil {
		return nil, nil, fmt.Errorf("job %s: unknown repository %q", jobName, job.Repository)
	}
	return job, rc, nil
}

// RestoreProject restores one Docker project ("job/project") from a
// snapshot: it stops the project, moves its current data aside, restores
// the project directory, bind mounts and volumes, starts the project again
// and (in dumps mode) loads the database dumps into fresh databases. If
// anything fails before the project is started again, the old data is put
// back.
func (e *Engine) RestoreProject(ctx context.Context, unitName string, o ProjectRestoreOptions) (*ProjectRestoreResult, error) {
	job, rc, err := e.dockerJob(unitName)
	if err != nil {
		return nil, err
	}
	say := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		e.Log.Info(msg, "project", unitName)
		if o.Progress != nil {
			o.Progress(msg)
		}
	}
	repo, err := e.Open(ctx, rc)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	lock, err := repo.Lock(ctx, true, "restore "+unitName)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	spec := o.Snapshot
	if spec == "" {
		spec = "latest"
	}
	snap, err := repo.FindSnapshot(ctx, spec, repository.SnapshotFilter{Job: unitName})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", unitName, err)
	}
	info := snap.Docker
	if info == nil {
		return nil, fmt.Errorf("snapshot %s has no Docker information", snap.ID().Short())
	}
	res := &ProjectRestoreResult{Project: info.Project, Snapshot: snap.ID().String(), Time: snap.Time.Format(time.RFC3339)}

	ropts := restorer.Options{Overwrite: "always", Owner: os.Geteuid() == 0}
	if o.Target != "" {
		res.Databases = "files"
		res.Paths = snap.Paths
		if o.DryRun {
			return res, nil
		}
		say("restoring snapshot %s below %s", snap.ID().Short(), o.Target)
		ropts.Target = o.Target
		r, err := restorer.Restore(ctx, repo, snap, "/", ropts)
		if err == nil && r.Errors > 0 {
			err = fmt.Errorf("%d files could not be restored", r.Errors)
		}
		return res, err
	}

	mode := o.Databases
	if mode == "" || mode == DBAuto {
		mode = DBFiles
		if !info.Stopped && slices.ContainsFunc(info.Dumps, func(d repository.DockerDump) bool { return d.File != "" }) {
			mode = DBDumps
		}
	}
	if mode != DBDumps && mode != DBFiles {
		return nil, fmt.Errorf("unknown database mode %q (auto, dumps, files)", o.Databases)
	}
	res.Databases = mode
	var dumps []repository.DockerDump
	if mode == DBDumps {
		for _, d := range info.Dumps {
			if d.File == "" {
				if d.Error == skipNoServer {
					continue
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: no dump in this snapshot (%s); its files are restored", d.Container, d.Error))
				continue
			}
			if d.DataDir == "" {
				res.Warnings = append(res.Warnings, d.Container+": database files are not on a mount; the dump is loaded over the existing data")
			}
			dumps = append(dumps, d)
		}
		if o.NoStart && len(dumps) > 0 {
			return nil, errors.New("loading dumps needs the containers running; use --databases files with --no-start")
		}
	}
	// Database directories are recreated empty and filled from the dumps.
	var freshDirs, excludes []string
	for _, d := range dumps {
		if d.DataDir != "" {
			freshDirs = append(freshDirs, d.DataDir)
			excludes = append(excludes, d.DataDir)
		}
	}
	var roots []string
	for _, p := range snap.Paths {
		if p != info.MetaDir {
			roots = append(roots, p)
		}
	}
	// The project directory goes first: Compose needs it to recreate
	// missing containers and volumes before their data is restored.
	hasWD := func(root string) bool { return info.WorkingDir != "" && within(info.WorkingDir, root) }
	slices.SortStableFunc(roots, func(a, b string) int {
		switch {
		case hasWD(a) && !hasWD(b):
			return -1
		case hasWD(b) && !hasWD(a):
			return 1
		}
		return 0
	})
	res.Paths = roots
	// Never replace the directory of another project: older snapshots of a
	// project that bind-mounts a parent directory (e.g. a deploy tool with
	// /srv/docker) contain other projects.
	if all, _, err := e.dockerProjects(ctx, job); err == nil {
		for _, root := range roots {
			for _, q := range all {
				if q.Name != info.Project && q.WorkingDir != "" && within(filepath.Clean(q.WorkingDir), root) {
					return nil, fmt.Errorf("%s contains project %s (%s); restoring it in place would replace that project too. Extract it with --target instead", root, q.Name, q.WorkingDir)
				}
			}
		}
	}
	if o.DryRun {
		return res, nil
	}

	dc := DockerClient(job)
	cur, existed, err := currentProject(ctx, dc, job, info)
	if err != nil {
		return nil, err
	}
	if existed && cur.Running() > 0 {
		say("stopping %s", info.Project)
		if err := dc.Stop(ctx, cur); err != nil {
			return nil, fmt.Errorf("stop: %w", err)
		}
	}

	stamp := time.Now().Format("20060102-150405")
	type moved struct{ path, old string }
	var asides []moved
	var restored []string
	rollback := func(cause error) error {
		say("restore failed, putting the previous data back: %v", cause)
		for _, p := range restored {
			_ = os.RemoveAll(p)
		}
		for i := len(asides) - 1; i >= 0; i-- {
			_ = os.RemoveAll(asides[i].path)
			if err := os.Rename(asides[i].old, asides[i].path); err != nil {
				cause = fmt.Errorf("%w; rollback of %s failed: %v (previous data is in %s)", cause, asides[i].path, err, asides[i].old)
			}
		}
		if existed {
			if err := dc.Start(context.WithoutCancel(ctx), cur); err != nil {
				cause = fmt.Errorf("%w; restart failed: %v", cause, err)
			}
		}
		return cause
	}
	created := false
	for _, root := range roots {
		if _, err := os.Lstat(root); err == nil {
			old := root + ".herakles-old-" + stamp
			if err := os.Rename(root, old); err != nil {
				return res, rollback(fmt.Errorf("move %s aside: %w", root, err))
			}
			asides = append(asides, moved{root, old})
		}
		say("restoring %s", root)
		ropts.Target = filepath.Dir(root)
		ropts.Excludes = nil
		for _, x := range excludes {
			if within(x, root) {
				ropts.Excludes = append(ropts.Excludes, x)
			}
		}
		r, err := restorer.Restore(ctx, repo, snap, root, ropts)
		restored = append(restored, root)
		if err == nil && r.Errors > 0 {
			err = fmt.Errorf("%d files could not be restored", r.Errors)
		}
		if err != nil {
			return res, rollback(fmt.Errorf("restore %s: %w", root, err))
		}
		if hasWD(root) && !existed && !info.Standalone {
			say("creating containers and volumes of %s", info.Project)
			if err := dc.Create(ctx, projectOf(info)); err != nil {
				return res, rollback(fmt.Errorf("compose create: %w", err))
			}
			created = true
		}
	}
	if !existed && !created && !info.Standalone && info.WorkingDir != "" {
		if err := dc.Create(ctx, projectOf(info)); err != nil {
			return res, rollback(fmt.Errorf("compose create: %w", err))
		}
		created = true
	}
	for _, d := range freshDirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return res, rollback(err)
		}
	}

	// Dumps are read from the snapshot into a private staging directory.
	var staging string
	if len(dumps) > 0 {
		staging, err = os.MkdirTemp(e.Config().GetStateDir(), "restore-")
		if err != nil {
			return res, rollback(err)
		}
		defer os.RemoveAll(staging)
		r, err := restorer.Restore(ctx, repo, snap, info.MetaDir, restorer.Options{Target: staging, Overwrite: "always"})
		if err == nil && r.Errors > 0 {
			err = fmt.Errorf("%d dump files could not be restored", r.Errors)
		}
		if err != nil {
			return res, rollback(fmt.Errorf("read dumps: %w", err))
		}
	}

	switch {
	case o.NoStart:
		say("leaving %s stopped", info.Project)
	case existed || created:
		say("starting %s", info.Project)
		if err := dc.Start(ctx, projectOf(info)); err != nil {
			return res, rollback(fmt.Errorf("start: %w", err))
		}
		res.Started = true
	default:
		res.Warnings = append(res.Warnings, "the container no longer exists; recreate it (see inspect.json in "+info.MetaDir+")")
	}

	// From here on the project runs with the restored data; failures are
	// reported and the previous data is kept for manual recovery.
	var loadErr error
	if res.Started && len(dumps) > 0 {
		now, _, err := currentProject(ctx, dc, job, info)
		if err != nil {
			loadErr = err
		}
		for _, d := range dumps {
			if loadErr != nil {
				break
			}
			i := slices.IndexFunc(now.Containers, func(c docker.Container) bool { return c.Name == d.Container })
			if i < 0 {
				loadErr = fmt.Errorf("container %s not found after start", d.Container)
				break
			}
			ct := now.Containers[i]
			say("waiting for %s", ct.Name)
			if err := waitStable(ctx, dc, ct, docker.DBKind(d.Kind)); err != nil {
				loadErr = err
				break
			}
			say("loading the %s dump into %s", d.Kind, ct.Name)
			f, err := os.Open(filepath.Join(staging, filepath.Base(info.MetaDir), d.File))
			if err != nil {
				loadErr = err
				break
			}
			err = dc.Load(ctx, ct, docker.DBKind(d.Kind), f)
			f.Close()
			if err != nil {
				loadErr = fmt.Errorf("load %s: %w", ct.Name, err)
				break
			}
			res.Loaded = append(res.Loaded, ct.Name)
		}
	}
	keep := o.KeepOld || loadErr != nil
	for _, a := range asides {
		if keep {
			res.OldCopies = append(res.OldCopies, a.old)
		} else if err := os.RemoveAll(a.old); err != nil {
			res.Warnings = append(res.Warnings, "could not remove "+a.old+": "+err.Error())
		}
	}
	if loadErr != nil {
		return res, fmt.Errorf("%w (the previous data is kept in %s)", loadErr, strings.Join(res.OldCopies, ", "))
	}
	say("restore of %s finished", info.Project)
	return res, nil
}

// waitStable waits until the database answers several times in a row:
// database images run a temporary server while initialising.
func waitStable(ctx context.Context, dc *docker.Client, ct docker.Container, kind docker.DBKind) error {
	deadline := time.Now().Add(5 * time.Minute)
	ok := 0
	for ok < 3 {
		if err := dc.Ready(ctx, ct, kind); err == nil {
			ok++
		} else {
			ok = 0
			if time.Now().After(deadline) {
				return fmt.Errorf("%s: database not ready: %w", ct.Name, err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil
}

// currentProject finds the live state of a project.
func currentProject(ctx context.Context, dc *docker.Client, job *config.Job, info *repository.DockerInfo) (docker.Project, bool, error) {
	cs, err := dc.Containers(ctx)
	if err != nil {
		return docker.Project{}, false, err
	}
	for _, p := range docker.Group(cs) {
		if p.Name == info.Project && p.Standalone == info.Standalone {
			return p, len(p.Containers) > 0, nil
		}
	}
	return projectOf(info), false, nil
}

func projectOf(info *repository.DockerInfo) docker.Project {
	return docker.Project{Name: info.Project, Standalone: info.Standalone, WorkingDir: info.WorkingDir, ConfigFiles: info.ConfigFiles}
}

func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}
