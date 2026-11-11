package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) initCmd() *cobra.Command {
	var genKey string
	var avg, min, max int
	cmd := &cobra.Command{
		Use:   "init [--repo NAME|LOCATION]",
		Short: "Create a new repository",
		Long: `Create a new encrypted repository. The repository is protected by a
password, a key file, or both (use --key-file / --generate-key-file).`,
		Example: `  herakles init --repo local
  herakles init --repo /mnt/usb/backup
  herakles init --repo sftp://backup@nas/srv/herakles --generate-key-file ~/.herakles.key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := a.repoConfig("")
			if err != nil {
				return err
			}
			if genKey != "" {
				if err := cryptoGenerateKeyFile(genKey); err != nil {
					return err
				}
				a.logf("generated key file %s — keep it safe, it is required to restore!", genKey)
				rc.KeyFile = genKey
			}
			if avg > 0 {
				rc.Chunker = config.ChunkerKiB{Min: min, Avg: avg, Max: max}
				if min == 0 {
					rc.Chunker.Min = avg / 2
				}
				if max == 0 {
					rc.Chunker.Max = avg * 8
				}
			}
			creds, err := rc.Credentials()
			if errors.Is(err, config.ErrNoPassword) {
				pw, perr := a.readPassword("New repository password: ")
				if perr != nil {
					return perr
				}
				pw2, perr := a.readPassword("Repeat password: ")
				if perr != nil {
					return perr
				}
				if pw != pw2 {
					return errors.New("passwords do not match")
				}
				if pw == "" {
					return errors.New("empty password")
				}
				creds.Password = pw
			} else if err != nil {
				return err
			}
			repo, err := a.eng.Init(cmd.Context(), rc, creds)
			if err != nil {
				return err
			}
			defer repo.Close()
			if a.jsonOut {
				return a.printJSON(map[string]any{"id": repo.Config().ID, "location": repo.Backend().Location()})
			}
			a.printf("created repository %s at %s\n", repo.Config().ID[:8], repo.Backend().Location())
			a.printf("WARNING: without the password/key file the data cannot be recovered.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&genKey, "generate-key-file", "", "create a random key file at this path and use it")
	cmd.Flags().IntVar(&avg, "chunk-avg-kib", 0, "average chunk size in KiB (power of two, default 1024)")
	cmd.Flags().IntVar(&min, "chunk-min-kib", 0, "minimum chunk size in KiB")
	cmd.Flags().IntVar(&max, "chunk-max-kib", 0, "maximum chunk size in KiB")
	return cmd
}

func (a *App) backupCmd() *cobra.Command {
	var typ string
	var all bool
	var paths, devices, excludes, tags []string
	var oneFS, excludeCaches bool
	cmd := &cobra.Command{
		Use:   "backup [JOB...]",
		Short: "Run backup jobs or an ad-hoc backup",
		Long: `Run configured backup jobs, or back up paths/devices directly.

Backup types:
  auto          incremental, or full when the last full backup is older than
                the job's full_interval (default)
  full          re-read and re-hash every file (no reliance on the previous
                snapshot); data is still deduplicated
  incremental   only read files changed since the latest snapshot of the job
  differential  only read files changed since the latest FULL snapshot

Every snapshot is complete and independently restorable.`,
		Example: `  herakles backup home
  herakles backup --all --type full
  herakles backup --repo local --path /etc --path /root --tag config
  herakles backup --repo offsite --device /dev/sda1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !config.ValidBackupType(typ) {
				return fmt.Errorf("invalid --type %q", typ)
			}
			ctx := cmd.Context()
			if len(paths) > 0 || len(devices) > 0 {
				if len(args) > 0 {
					return errors.New("give either job names or --path/--device, not both")
				}
				job := &config.Job{Name: "", Paths: paths, Devices: devices, Exclude: excludes, Tags: tags, OneFileSystem: oneFS, ExcludeCaches: excludeCaches}
				return a.adhocBackup(cmd, job, typ)
			}
			jobs := args
			if all {
				for _, j := range a.cfg.Jobs {
					if !j.Disabled {
						jobs = append(jobs, j.Name)
					}
				}
			}
			if len(jobs) == 0 {
				return errors.New("no job given (use 'herakles backup JOB', --all, or --path/--device with --repo)")
			}
			var failed, warned int
			var results []*engine.JobResult
			for _, name := range jobs {
				prog := &archiver.Progress{}
				pp := a.startProgress(name, prog)
				res, err := a.eng.RunJob(ctx, name, engine.RunOptions{Type: typ, Progress: prog, OnError: func(p string, err error) {
					if !a.jsonOut {
						fmt.Fprintf(a.err, "\r\033[Kwarning: %s: %v\n", p, err)
					}
				}})
				pp.Stop()
				if res != nil {
					results = append(results, res)
				}
				if err != nil {
					failed++
					if !a.jsonOut {
						fmt.Fprintf(a.err, "job %s failed: %v\n", name, err)
					}
					if ctx.Err() != nil {
						break
					}
					continue
				}
				if res.Status == "warning" {
					warned++
				}
				a.printResult(res)
			}
			if a.jsonOut {
				_ = a.printJSON(results)
			}
			switch {
			case failed > 0:
				return exitError{1, fmt.Errorf("%d of %d jobs failed", failed, len(jobs))}
			case warned > 0:
				return exitError{ExitWarning, errors.New("some files could not be read")}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&typ, "type", "t", "auto", "backup type: auto, full, incremental, differential")
	f.BoolVar(&all, "all", false, "run all enabled jobs")
	f.StringArrayVar(&paths, "path", nil, "ad-hoc: path to back up (repeatable)")
	f.StringArrayVar(&devices, "device", nil, "ad-hoc: block device/partition to image (repeatable)")
	f.StringArrayVarP(&excludes, "exclude", "e", nil, "ad-hoc: exclude pattern (repeatable)")
	f.StringArrayVar(&tags, "tag", nil, "ad-hoc: tag for the snapshot (repeatable)")
	f.BoolVarP(&oneFS, "one-file-system", "x", false, "ad-hoc: do not cross filesystem boundaries")
	f.BoolVar(&excludeCaches, "exclude-caches", false, "ad-hoc: skip directories with CACHEDIR.TAG")
	return cmd
}

func (a *App) printResult(res *engine.JobResult) {
	if res.Summary == nil {
		return
	}
	s := res.Summary
	a.printf("job %s: %s backup -> snapshot %s (%s)\n", res.Job, res.Type, res.Snapshot[:8], res.Status)
	a.printf("  files: %d new, %d changed, %d unmodified (%d total, %s)\n", s.FilesNew, s.FilesChanged, s.FilesUnmodified, s.TotalFiles, ui.Bytes(s.TotalBytes))
	a.printf("  added: %s of new data, %s stored after compression; took %s\n", ui.Bytes(s.BytesAdded), ui.Bytes(s.BytesStored), ui.Duration(s.End.Sub(s.Start)))
	if s.Errors > 0 {
		a.printf("  %d files could not be read\n", s.Errors)
	}
	if res.Forgotten > 0 {
		a.printf("  retention: forgot %d snapshots\n", res.Forgotten)
	}
	if res.Pruned != nil {
		a.printf("  prune: freed %s\n", ui.Bytes(res.Pruned.BytesFreed))
	}
}

func (a *App) adhocBackup(cmd *cobra.Command, job *config.Job, typ string) error {
	ctx := cmd.Context()
	repo, _, err := a.openRepo(ctx, "")
	if err != nil {
		return err
	}
	defer repo.Close()
	lock, err := repo.Lock(ctx, false, "backup")
	if err != nil {
		return err
	}
	defer lock.Unlock()
	snaps, err := repo.Snapshots(ctx)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	var abs []string
	for _, p := range job.Paths {
		ap, err := filepath.Abs(config.ExpandHome(p))
		if err != nil {
			return err
		}
		abs = append(abs, ap)
	}
	// Ad-hoc backups use the latest snapshot with the same paths as parent.
	var parent *repository.Snapshot
	sort.Strings(abs)
	if typ != "full" {
		for i := len(snaps) - 1; i >= 0; i-- {
			s := snaps[i]
			if s.Job == "" && s.Hostname == host && sameStrings(s.Paths, append(append([]string(nil), abs...), job.Devices...)) {
				parent = s
				break
			}
		}
	}
	rtyp := repository.TypeIncremental
	if typ == "full" || parent == nil {
		rtyp = repository.TypeFull
	}
	prog := &archiver.Progress{}
	ar, err := archiver.New(repo, archiver.Options{Paths: abs, Devices: job.Devices, Excludes: job.Exclude, Tags: job.Tags,
		OneFileSystem: job.OneFileSystem, ExcludeCaches: job.ExcludeCaches, Type: rtyp, Parent: parent, Progress: prog,
		OnError: func(p string, err error) {
			if !a.jsonOut {
				fmt.Fprintf(a.err, "\r\033[Kwarning: %s: %v\n", p, err)
			}
		}})
	if err != nil {
		return err
	}
	pp := a.startProgress("backup", prog)
	snap, err := ar.Run(ctx)
	pp.Stop()
	if err != nil {
		return err
	}
	res := &engine.JobResult{Job: "(ad-hoc)", Type: rtyp, Snapshot: snap.ID().String(), Summary: snap.Summary, Status: "success"}
	if snap.Summary.Errors > 0 {
		res.Status = "warning"
	}
	if a.jsonOut {
		return a.printJSON(res)
	}
	a.printResult(res)
	if snap.Summary.Errors > 0 {
		return exitError{ExitWarning, errors.New("some files could not be read")}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func (a *App) jobsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "jobs",
		Short: "List configured jobs with schedules and last results",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			h := a.eng.History()
			if a.jsonOut {
				type jv struct {
					*config.Job
					Last *engine.JobResult `json:"last,omitempty"`
				}
				var out []jv
				for _, j := range a.cfg.Jobs {
					out = append(out, jv{j, h.Last(j.Name)})
				}
				return a.printJSON(out)
			}
			if len(a.cfg.Jobs) == 0 {
				a.printf("no jobs configured in %s\n", a.cfg.Path())
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "JOB\tREPOSITORY\tSOURCES\tSCHEDULE\tLAST RUN\tSTATUS")
			for _, j := range a.cfg.Jobs {
				var sched []string
				for _, s := range j.Schedules {
					t := s.Type
					if t == "" {
						t = "auto"
					}
					sched = append(sched, s.Cron+" ("+t+")")
				}
				if j.Disabled {
					sched = []string{"disabled"}
				}
				last, status := "never", "-"
				if r := h.Last(j.Name); r != nil {
					last, status = ui.Ago(r.Start), r.Status
				}
				src := append(append([]string(nil), j.Paths...), j.Devices...)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", j.Name, j.Repository, strings.Join(src, ", "), strings.Join(sched, "; "), last, status)
			}
			return tw.Flush()
		},
	}
}
