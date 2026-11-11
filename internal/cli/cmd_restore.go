package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) restoreCmd() *cobra.Command {
	var target, overwrite string
	var includes, excludes []string
	var verify, dryRun, owner bool
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "restore SNAPSHOT[:PATH] --target DIR",
		Short: "Restore files from a snapshot",
		Long: `Restore a snapshot (or a path inside it) into a target directory.
Use "latest" for the newest snapshot (combine with --job/--host/--tag).
Device images are restored as image files; use 'restore-device' to write
them back to a disk or partition.`,
		Example: `  herakles restore latest --target /tmp/restore
  herakles restore 3fa2b1c9:/home/alice/Documents --target ~/recovered
  herakles restore latest --job home --include '*.pdf' --target /tmp/pdfs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if target == "" {
				return errors.New("--target is required")
			}
			spec, p := parseSnapSpec(args[0])
			if p == "" {
				p = "/"
			}
			repo, _, err := a.openRepo(ctx, a.jobRepo(f.job))
			if err != nil {
				return err
			}
			defer repo.Close()
			snap, err := repo.FindSnapshot(ctx, spec, f.filter())
			if err != nil {
				return err
			}
			prog := &restorer.Progress{}
			var stop atomic.Bool
			done := make(chan struct{})
			if !a.quiet && !a.jsonOut && a.isTTY() {
				go func() {
					start := time.Now()
					for !stop.Load() {
						time.Sleep(250 * time.Millisecond)
						tot := prog.BytesTotal.Load()
						pct := 0.0
						if tot > 0 {
							pct = 100 * float64(prog.BytesDone.Load()) / float64(tot)
						}
						fmt.Fprintf(a.err, "\r\033[Krestore [%s] %5.1f%% %d/%d files, %s/%s", ui.Duration(time.Since(start)), pct, prog.FilesDone.Load(), prog.FilesTotal.Load(), ui.Bytes(prog.BytesDone.Load()), ui.Bytes(tot))
					}
					fmt.Fprint(a.err, "\r\033[K")
					close(done)
				}()
			} else {
				close(done)
			}
			res, err := restorer.Restore(ctx, repo, snap, p, restorer.Options{
				Target: target, Includes: includes, Excludes: excludes, Overwrite: overwrite, Verify: verify, DryRun: dryRun, Owner: owner, Progress: prog,
				OnError: func(path string, err error) { fmt.Fprintf(a.err, "\r\033[Kerror: %s: %v\n", path, err) },
			})
			stop.Store(true)
			<-done
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(res)
			}
			a.printf("restored %d files, %d directories (%s) from snapshot %s to %s\n", res.Files, res.Dirs, ui.Bytes(res.Bytes), snap.ID().Short(), target)
			if res.Skipped > 0 {
				a.printf("skipped %d existing files\n", res.Skipped)
			}
			if verify {
				a.printf("verified %d files\n", res.Verified)
			}
			if res.Errors > 0 {
				return exitError{1, fmt.Errorf("%d errors during restore", res.Errors)}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&target, "target", "t", "", "directory to restore into")
	fl.StringArrayVarP(&includes, "include", "i", nil, "only restore matching paths (repeatable)")
	fl.StringArrayVarP(&excludes, "exclude", "e", nil, "skip matching paths (repeatable)")
	fl.StringVar(&overwrite, "overwrite", "always", "existing files: always, if-changed, never")
	fl.BoolVar(&verify, "verify", false, "verify restored files against the snapshot")
	fl.BoolVarP(&dryRun, "dry-run", "n", false, "do not write anything")
	fl.BoolVar(&owner, "owner", true, "restore file ownership (when running as root)")
	f.add(cmd)
	return cmd
}

// jobRepo returns the repository of a job (for --job convenience).
func (a *App) jobRepo(job string) string {
	if a.repoFlag != "" || job == "" {
		return ""
	}
	if j := a.cfg.Job(job); j != nil {
		return j.Repository
	}
	return ""
}

func (a *App) restoreDeviceCmd() *cobra.Command {
	var to string
	var force bool
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "restore-device SNAPSHOT:PATH --to DEVICE",
		Short: "Write a backed up disk/partition image to a device or image file",
		Example: `  herakles restore-device latest:/dev/sda1 --to /dev/sdb1 --force
  herakles restore-device 3fa2b1c9:/dev/nvme0n1p2 --to /tmp/root.img`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			spec, p := parseSnapSpec(args[0])
			if p == "" || to == "" {
				return errors.New("usage: restore-device SNAPSHOT:PATH --to DEVICE")
			}
			if fi, err := os.Stat(to); err == nil && fi.Mode()&os.ModeDevice != 0 && !force {
				return fmt.Errorf("%s is a device; all data on it will be overwritten. Re-run with --force", to)
			}
			repo, _, err := a.openRepo(ctx, a.jobRepo(f.job))
			if err != nil {
				return err
			}
			defer repo.Close()
			snap, err := repo.FindSnapshot(ctx, spec, f.filter())
			if err != nil {
				return err
			}
			node, err := repo.FindNode(ctx, snap.Tree, p)
			if err != nil {
				return err
			}
			start := time.Now()
			err = restorer.RestoreDevice(ctx, repo, node, to, func(done, total int64) {
				if a.isTTY() && !a.quiet {
					fmt.Fprintf(a.err, "\r\033[Kwriting %s: %s / %s", to, ui.Bytes(done), ui.Bytes(total))
				}
			})
			if a.isTTY() {
				fmt.Fprint(a.err, "\r\033[K")
			}
			if err != nil {
				return err
			}
			a.printf("wrote %s to %s in %s\n", ui.Bytes(int64(node.Size)), to, ui.Duration(time.Since(start)))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target device or image file")
	cmd.Flags().BoolVar(&force, "force", false, "allow overwriting a block device")
	f.add(cmd)
	return cmd
}

func (a *App) dumpCmd() *cobra.Command {
	var asTar bool
	var output string
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "dump SNAPSHOT[:PATH] [PATH]",
		Short: "Write a file (or a directory as tar) from a snapshot to stdout",
		Example: `  herakles dump latest /etc/fstab
  herakles dump latest:/home/alice/projects --tar > projects.tar`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			spec, p := parseSnapSpec(args[0])
			if len(args) == 2 {
				p = args[1]
			}
			repo, _, err := a.openRepo(ctx, a.jobRepo(f.job))
			if err != nil {
				return err
			}
			defer repo.Close()
			snap, err := repo.FindSnapshot(ctx, spec, f.filter())
			if err != nil {
				return err
			}
			node, err := repo.FindNode(ctx, snap.Tree, p)
			if err != nil {
				return err
			}
			var w io.Writer = a.out
			if output != "" {
				fh, err := os.Create(filepath.Clean(output))
				if err != nil {
					return err
				}
				defer fh.Close()
				w = fh
			}
			if asTar || node.Type == repository.NodeDir {
				return restorer.DumpTar(ctx, repo, node, w)
			}
			return restorer.DumpFile(ctx, repo, node, w)
		},
	}
	cmd.Flags().BoolVar(&asTar, "tar", false, "write a tar archive (default for directories)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to file instead of stdout")
	f.add(cmd)
	return cmd
}
