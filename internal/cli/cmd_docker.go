package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) dockerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker",
		Short: "Back up and restore Docker Compose projects one by one",
		Long: `Jobs with a docker section back up every Docker Compose project (and
standalone container) as its own snapshot named JOB/PROJECT: the project
directory, bind mounts, named volumes and dumps of its Postgres,
MySQL/MariaDB and MongoDB databases.

PROJECT may be given as JOB/PROJECT, or just PROJECT when only one job has
a docker section.`,
	}
	var withRepo bool
	ls := &cobra.Command{
		Use: "ls [JOB]", Short: "List the projects a docker job backs up", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			job, err := a.dockerJob(args)
			if err != nil {
				return err
			}
			ps, err := a.eng.DockerProjects(cmd.Context(), job)
			if err != nil {
				return err
			}
			last := map[string]*repository.Snapshot{}
			if withRepo {
				repo, _, err := a.openRepo(cmd.Context(), job.Repository)
				if err != nil {
					return err
				}
				all, err := repo.Snapshots(cmd.Context())
				repo.Close()
				if err != nil {
					return err
				}
				for _, s := range all {
					if l := last[s.Job]; l == nil || s.Time.After(l.Time) {
						last[s.Job] = s
					}
				}
			}
			if a.jsonOut {
				type row struct {
					docker.Project
					Data         docker.DataSet       `json:"data"`
					Databases    []string             `json:"databases,omitempty"`
					LastSnapshot *repository.Snapshot `json:"last_snapshot,omitempty"`
				}
				var rows []row
				for _, p := range ps {
					rows = append(rows, row{Project: p, Data: p.Data(dataOpts(job)), Databases: databases(p), LastSnapshot: last[job.Name+"/"+p.Name]})
				}
				return a.printJSON(rows)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			head := "PROJECT\tCONTAINERS\tDATABASES\tDATA PATHS\tMODE"
			if withRepo {
				head += "\tLAST BACKUP"
			}
			fmt.Fprintln(tw, head)
			for _, p := range ps {
				mode := "dump"
				switch {
				case job.Docker.Stops(p.Name):
					mode = "stop"
				case job.Docker.SkipDumps || len(databases(p)) == 0:
					mode = "-"
				}
				kind := ""
				if p.Standalone {
					kind = " (standalone)"
				}
				line := fmt.Sprintf("%s%s\t%d/%d running\t%s\t%d\t%s", p.Name, kind, p.Running(), len(p.Containers),
					dash(strings.Join(databases(p), ", ")), len(p.Data(dataOpts(job)).Paths), mode)
				if withRepo {
					if s := last[job.Name+"/"+p.Name]; s != nil {
						line += fmt.Sprintf("\t%s (%s, %s)", ui.Time(s.Time), s.Type, s.ID().Short())
					} else {
						line += "\tnever"
					}
				}
				fmt.Fprintln(tw, line)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if all, vs, err := engine.DockerClient(job).ProjectsAndVolumes(cmd.Context(), engine.DockerSearch(job)); err == nil {
				if orphans := docker.Orphans(all, vs); len(orphans) > 0 {
					a.printf("\nvolumes outside every project (not backed up unless listed in the job's paths):\n")
					for _, v := range orphans {
						a.printf("  %s  %s\n", v.Name, v.Mountpoint)
					}
				}
			}
			return nil
		},
	}
	ls.Flags().BoolVar(&withRepo, "last", false, "also show the latest snapshot of each project (opens the repository)")

	show := &cobra.Command{
		Use: "show [JOB/]PROJECT", Short: "Show what is backed up for a project", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			job, name, err := a.dockerUnit(args[0])
			if err != nil {
				return err
			}
			ps, err := a.eng.DockerProjects(cmd.Context(), job)
			if err != nil {
				return err
			}
			for _, p := range ps {
				if p.Name != name {
					continue
				}
				ds := p.Data(dataOpts(job))
				if a.jsonOut {
					return a.printJSON(map[string]any{"project": p, "data": ds, "databases": databases(p)})
				}
				a.printf("project %s (snapshots: %s/%s)\n", p.Name, job.Name, p.Name)
				if p.WorkingDir != "" {
					a.printf("  directory:  %s\n", p.WorkingDir)
				}
				for _, c := range p.Containers {
					state := "stopped"
					if c.Running {
						state = "running"
					}
					db := ""
					if k, ok := docker.DetectDB(c); ok {
						db = fmt.Sprintf("  [%s database, data in %s]", k, dash(docker.DataDir(c, k)))
					}
					a.printf("  container:  %s (%s, %s)%s\n", c.Name, c.Image, state, db)
				}
				for _, v := range sortedKeys(ds.Volumes) {
					a.printf("  volume:     %s -> %s\n", v, ds.Volumes[v])
				}
				for _, b := range ds.Binds {
					a.printf("  bind mount: %s\n", b)
				}
				a.printf("  backed up:  %s\n", strings.Join(ds.Paths, ", "))
				return nil
			}
			return fmt.Errorf("job %s has no project %q", job.Name, name)
		},
	}

	var ro engine.ProjectRestoreOptions
	var yes bool
	restore := &cobra.Command{
		Use:   "restore [JOB/]PROJECT",
		Short: "Restore a project from its snapshot (stops and restarts it)",
		Long: `Restore a project in place: stop it, move its current data aside, restore
the project directory, bind mounts and volumes, start it again and load
the database dumps. If something fails before the project runs again, the
previous data is put back.

Database modes:
  auto   files for snapshots taken with the project stopped, else dumps
  dumps  start with empty database directories and load the dumps
  files  restore the raw database files

With --target, everything is restored below that directory instead and
Docker is not touched (to inspect a snapshot or move it elsewhere).`,
		Example: "  herakles docker restore fundus\n  herakles docker restore docker/fundus --snapshot 3fa2b1c9 --databases files\n  herakles docker restore wekan --target /tmp/wekan-restore",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			job, name, err := a.dockerUnit(args[0])
			if err != nil {
				return err
			}
			unit := job.Name + "/" + name
			plan := ro
			plan.DryRun = true
			p, err := a.eng.RestoreProject(cmd.Context(), unit, plan)
			if err != nil {
				return err
			}
			if a.jsonOut && ro.DryRun {
				return a.printJSON(p)
			}
			a.printf("snapshot %s of %s from %s\n", p.Snapshot[:8], p.Project, p.Time)
			if ro.Target != "" {
				a.printf("restoring all files below %s (Docker is not touched)\n", ro.Target)
			} else {
				a.printf("this replaces the current data of %s:\n", p.Project)
				for _, x := range p.Paths {
					a.printf("  %s\n", x)
				}
				a.printf("databases: %s\n", p.Databases)
			}
			for _, w := range p.Warnings {
				a.printf("note: %s\n", w)
			}
			if ro.DryRun {
				return nil
			}
			if ro.Target == "" && !yes {
				if !a.isTTY() {
					return errors.New("confirm with --yes (the project's current data will be replaced)")
				}
				fmt.Fprintf(a.err, "Type the project name to continue: ")
				line, _ := bufio.NewReader(a.in).ReadString('\n')
				if strings.TrimSpace(line) != p.Project {
					return errors.New("aborted")
				}
			}
			run := ro
			run.Progress = func(m string) { a.logf("%s", m) }
			res, err := a.eng.RestoreProject(cmd.Context(), unit, run)
			if res != nil {
				for _, w := range res.Warnings {
					a.printf("warning: %s\n", w)
				}
				for _, o := range res.OldCopies {
					a.printf("previous data kept in %s\n", o)
				}
			}
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(res)
			}
			a.printf("restored %s from %s", res.Project, res.Snapshot[:8])
			if len(res.Loaded) > 0 {
				a.printf(", loaded dumps into %s", strings.Join(res.Loaded, ", "))
			}
			a.printf("\n")
			return nil
		},
	}
	f := restore.Flags()
	f.StringVar(&ro.Snapshot, "snapshot", "latest", "snapshot ID (prefix) or latest")
	f.StringVar(&ro.Databases, "databases", engine.DBAuto, "auto, dumps or files")
	f.BoolVar(&ro.NoStart, "no-start", false, "leave the project stopped (with --databases files)")
	f.BoolVar(&ro.KeepOld, "keep-old", false, "keep the replaced data as PATH.herakles-old-TIME")
	f.StringVar(&ro.Target, "target", "", "restore below this directory instead; Docker is not touched")
	f.BoolVarP(&ro.DryRun, "dry-run", "n", false, "only show what would be restored")
	f.BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")

	var typ string
	backup := &cobra.Command{
		Use: "backup [JOB/]PROJECT...", Short: "Back up projects now", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				job, name, err := a.dockerUnit(arg)
				if err != nil {
					return err
				}
				if err := a.runBackupJob(cmd.Context(), job.Name+"/"+name, typ); err != nil {
					return err
				}
			}
			return nil
		},
	}
	backup.Flags().StringVarP(&typ, "type", "t", "auto", "backup type: auto, full, incremental, differential")
	cmd.AddCommand(ls, show, backup, restore)
	return cmd
}

func dataOpts(job *config.Job) docker.DataOptions {
	return docker.DataOptions{SkipVolumes: job.Docker.SkipVolumes, SkipBindMounts: job.Docker.SkipBindMounts}
}

func databases(p docker.Project) []string {
	var out []string
	for _, c := range p.Containers {
		if k, ok := docker.DetectDB(c); ok {
			out = append(out, string(k))
		}
	}
	return out
}

// dockerJob returns the named docker job, or the only one.
func (a *App) dockerJob(args []string) (*config.Job, error) {
	if len(args) > 0 {
		j := a.cfg.Job(args[0])
		if j == nil || j.Docker == nil {
			return nil, fmt.Errorf("%q is not a job with a docker section", args[0])
		}
		return j, nil
	}
	var found []*config.Job
	for _, j := range a.cfg.Jobs {
		if j.Docker != nil {
			found = append(found, j)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, errors.New("no job has a docker section (see 'herakles config example')")
	}
	return nil, errors.New("several docker jobs; name one")
}

// dockerUnit resolves "[JOB/]PROJECT".
func (a *App) dockerUnit(arg string) (*config.Job, string, error) {
	if job, project := config.SplitUnit(arg); project != "" {
		j, err := a.dockerJob([]string{job})
		return j, project, err
	}
	j, err := a.dockerJob(nil)
	return j, arg, err
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// runBackupJob runs one job (or JOB/PROJECT) with a progress line.
func (a *App) runBackupJob(ctx context.Context, name, typ string) error {
	prog := &archiver.Progress{}
	pp := a.startProgress(name, prog)
	res, err := a.eng.RunJob(ctx, name, engine.RunOptions{Type: typ, Progress: prog})
	pp.Stop()
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(res)
	}
	a.printResult(res)
	if res.Status == "warning" {
		return exitError{ExitWarning, errors.New(res.Error)}
	}
	return nil
}
