package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/retention"
	"github.com/lamisator/herakles/internal/ui"
)

type snapFilterFlags struct {
	job, host string
	tags      []string
}

func (f *snapFilterFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.job, "job", "", "only snapshots of this job")
	cmd.Flags().StringVar(&f.host, "host", "", "only snapshots of this host")
	cmd.Flags().StringArrayVar(&f.tags, "tag", nil, "only snapshots with this tag (repeatable)")
}

func (f *snapFilterFlags) filter() repository.SnapshotFilter {
	return repository.SnapshotFilter{Job: f.job, Hostname: f.host, Tags: f.tags}
}

// SnapshotView is the JSON representation of a snapshot.
type SnapshotView struct {
	ID string `json:"id"`
	*repository.Snapshot
}

func (a *App) snapshotsCmd() *cobra.Command {
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:     "snapshots",
		Aliases: []string{"list", "ls-snapshots"},
		Short:   "List snapshots",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			all, err := repo.Snapshots(cmd.Context())
			if err != nil {
				return err
			}
			snaps := repository.FilterSnapshots(all, f.filter())
			if a.jsonOut {
				out := []SnapshotView{}
				for _, s := range snaps {
					out = append(out, SnapshotView{s.ID().String(), s})
				}
				return a.printJSON(out)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTIME\tJOB\tHOST\tTYPE\tSIZE\tADDED\tTAGS\tPATHS")
			for _, s := range snaps {
				size, added := "-", "-"
				if s.Summary != nil {
					size, added = ui.Bytes(s.Summary.TotalBytes), ui.Bytes(s.Summary.BytesStored)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID().Short(), ui.Time(s.Time), dash(s.Job), s.Hostname, s.Type, size, added, strings.Join(s.Tags, ","), strings.Join(s.Paths, ", "))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			a.printf("%d snapshots\n", len(snaps))
			return nil
		},
	}
	f.add(cmd)
	return cmd
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func modeString(n *repository.Node) string {
	return n.Mode.String()
}

func (a *App) lsCmd() *cobra.Command {
	var long, recursive bool
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "ls SNAPSHOT[:PATH] [PATH]",
		Short: "List files in a snapshot",
		Example: `  herakles ls latest
  herakles ls 3fa2b1c9 /home/alice -l
  herakles ls latest:/etc --recursive`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			spec, p := parseSnapSpec(args[0])
			if len(args) == 2 {
				p = args[1]
			}
			repo, _, err := a.openRepo(ctx, "")
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
			base := "/" + strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/")
			if base == "/" {
				base = ""
			}
			type entry struct {
				Path string           `json:"path"`
				Node *repository.Node `json:"node"`
			}
			var entries []entry
			emit := func(path string, n *repository.Node) {
				if a.jsonOut {
					entries = append(entries, entry{path, n})
					return
				}
				if long {
					size := fmt.Sprint(n.Size)
					if n.Type == repository.NodeDir {
						size = "-"
					}
					extra := ""
					if n.Type == repository.NodeSymlink {
						extra = " -> " + n.LinkTarget
					}
					owner := n.User
					if owner == "" {
						owner = fmt.Sprint(n.UID)
					}
					fmt.Fprintf(a.out, "%s %8s %12s %s %s%s\n", modeString(n), owner, size, ui.Time(n.ModTime), path, extra)
				} else {
					fmt.Fprintln(a.out, path)
				}
			}
			if node.Type != repository.NodeDir {
				emit(base, node)
			} else {
				err = repo.Walk(ctx, *node.Subtree, func(path string, n *repository.Node) error {
					emit(base+path, n)
					if !recursive && n.Type == repository.NodeDir {
						return repository.SkipDir
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			if a.jsonOut {
				return a.printJSON(entries)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&long, "long", "l", false, "long listing")
	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "list recursively")
	f.add(cmd)
	return cmd
}

func (a *App) findCmd() *cobra.Command {
	var f snapFilterFlags
	var limit int
	cmd := &cobra.Command{
		Use:     "find PATTERN",
		Short:   "Search for files in all snapshots",
		Example: "  herakles find '*.docx'\n  herakles find '**/projects/*/README.md' --job home",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, _, err := a.openRepo(ctx, "")
			if err != nil {
				return err
			}
			defer repo.Close()
			all, err := repo.Snapshots(ctx)
			if err != nil {
				return err
			}
			matches, err := restorer.Find(ctx, repo, repository.FilterSnapshots(all, f.filter()), args[0], limit)
			if err != nil {
				return err
			}
			if a.jsonOut {
				type m struct {
					Snapshot string    `json:"snapshot"`
					Time     string    `json:"time"`
					Path     string    `json:"path"`
					Size     uint64    `json:"size"`
					Type     string    `json:"type"`
				}
				out := []m{}
				for _, x := range matches {
					out = append(out, m{x.Snapshot.ID().String(), x.Snapshot.Time.String(), x.Path, x.Node.Size, string(x.Node.Type)})
				}
				return a.printJSON(out)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SNAPSHOT\tTIME\tSIZE\tMODIFIED\tPATH")
			for _, x := range matches {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", x.Snapshot.ID().Short(), ui.Time(x.Snapshot.Time), ui.Bytes(int64(x.Node.Size)), ui.Time(x.Node.ModTime), x.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 1000, "maximum number of results (0 = unlimited)")
	f.add(cmd)
	return cmd
}

func (a *App) diffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff SNAPSHOT1 SNAPSHOT2",
		Short: "Show differences between two snapshots",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, _, err := a.openRepo(ctx, "")
			if err != nil {
				return err
			}
			defer repo.Close()
			all, err := repo.Snapshots(ctx)
			if err != nil {
				return err
			}
			s1, err := repository.ResolveSnapshot(all, args[0], repository.SnapshotFilter{})
			if err != nil {
				return err
			}
			s2, err := repository.ResolveSnapshot(all, args[1], repository.SnapshotFilter{})
			if err != nil {
				return err
			}
			changes, err := restorer.Diff(ctx, repo, s1.Tree, s2.Tree)
			if err != nil {
				return err
			}
			if a.jsonOut {
				if changes == nil {
					changes = []restorer.Change{}
				}
				return a.printJSON(changes)
			}
			var added, removed, modified int
			for _, c := range changes {
				sym := map[string]string{"added": "+", "removed": "-", "modified": "M", "type": "T"}[c.Kind]
				if c.Metadata {
					sym = "U"
				}
				switch c.Kind {
				case "added":
					added++
				case "removed":
					removed++
				default:
					modified++
				}
				fmt.Fprintf(a.out, "%s  %s\n", sym, c.Path)
			}
			a.printf("\n%d added, %d removed, %d modified\n", added, removed, modified)
			return nil
		},
	}
}

type policyFlags struct {
	p                                                     retention.Policy
	within, withinH, withinD, withinW, withinM, withinY string
}

func (pf *policyFlags) add(cmd *cobra.Command) {
	f := cmd.Flags()
	f.IntVar(&pf.p.KeepLast, "keep-last", 0, "keep the last N snapshots")
	f.IntVar(&pf.p.KeepHourly, "keep-hourly", 0, "keep the last N hourly snapshots")
	f.IntVar(&pf.p.KeepDaily, "keep-daily", 0, "keep the last N daily snapshots")
	f.IntVar(&pf.p.KeepWeekly, "keep-weekly", 0, "keep the last N weekly snapshots")
	f.IntVar(&pf.p.KeepMonthly, "keep-monthly", 0, "keep the last N monthly snapshots")
	f.IntVar(&pf.p.KeepYearly, "keep-yearly", 0, "keep the last N yearly snapshots")
	f.IntVar(&pf.p.KeepFull, "keep-full", 0, "keep the last N full backups")
	f.StringVar(&pf.within, "keep-within", "", "keep everything within duration (e.g. 14d, 1y2m)")
	f.StringVar(&pf.withinH, "keep-within-hourly", "", "keep hourly snapshots within duration")
	f.StringVar(&pf.withinD, "keep-within-daily", "", "keep daily snapshots within duration")
	f.StringVar(&pf.withinW, "keep-within-weekly", "", "keep weekly snapshots within duration")
	f.StringVar(&pf.withinM, "keep-within-monthly", "", "keep monthly snapshots within duration")
	f.StringVar(&pf.withinY, "keep-within-yearly", "", "keep yearly snapshots within duration")
	f.StringArrayVar(&pf.p.KeepTags, "keep-tag", nil, "keep snapshots with this tag")
	f.StringVar(&pf.p.GroupBy, "group-by", "", "group snapshots by job,host,paths,tags (default job,host)")
}

func (pf *policyFlags) policy() (retention.Policy, error) {
	p := pf.p
	for _, x := range []struct {
		s string
		d *retention.Duration
	}{{pf.within, &p.KeepWithin}, {pf.withinH, &p.KeepWithinHourly}, {pf.withinD, &p.KeepWithinDaily}, {pf.withinW, &p.KeepWithinWeekly}, {pf.withinM, &p.KeepWithinMonthly}, {pf.withinY, &p.KeepWithinYearly}} {
		if x.s == "" {
			continue
		}
		d, err := retention.ParseDuration(x.s)
		if err != nil {
			return p, err
		}
		*x.d = d
	}
	return p, p.Validate()
}

func (a *App) forgetCmd() *cobra.Command {
	var pf policyFlags
	var f snapFilterFlags
	var dryRun, prune bool
	cmd := &cobra.Command{
		Use:   "forget [SNAPSHOT...]",
		Short: "Remove snapshots explicitly or by retention policy",
		Long: `Remove snapshots. Give snapshot IDs to remove them explicitly, or use
--keep-* flags, or --job to apply the job's configured retention policy.
Data is only freed by 'prune' (or --prune).`,
		Example: `  herakles forget --job home --dry-run
  herakles forget --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --prune
  herakles forget 3fa2b1c9`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repoName := ""
			pol, err := pf.policy()
			if err != nil {
				return err
			}
			if f.job != "" && pol.Empty() && len(args) == 0 {
				j := a.cfg.Job(f.job)
				if j == nil {
					return fmt.Errorf("unknown job %q", f.job)
				}
				pol = j.Retention
				repoName = j.Repository
				if pol.Empty() {
					return fmt.Errorf("job %s has no retention policy", f.job)
				}
			}
			if f.job != "" && repoName == "" && a.repoFlag == "" {
				if j := a.cfg.Job(f.job); j != nil {
					repoName = j.Repository
				}
			}
			repo, rc, err := a.openRepo(ctx, repoName)
			if err != nil {
				return err
			}
			defer repo.Close()
			lock, err := repo.Lock(ctx, false, "forget")
			if err != nil {
				return err
			}
			all, err := repo.Snapshots(ctx)
			if err != nil {
				lock.Unlock()
				return err
			}
			var remove []*repository.Snapshot
			if len(args) > 0 {
				for _, spec := range args {
					s, err := repository.ResolveSnapshot(all, spec, repository.SnapshotFilter{})
					if err != nil {
						lock.Unlock()
						return err
					}
					remove = append(remove, s)
				}
			} else {
				if pol.Empty() {
					lock.Unlock()
					return errors.New("no policy given: refusing to forget all snapshots (use --keep-* or --job)")
				}
				groups := retention.Apply(repository.FilterSnapshots(all, f.filter()), pol)
				if !a.jsonOut && !a.quiet {
					for _, g := range groups {
						fmt.Fprintf(a.out, "group %s:\n", g.Key)
						for _, d := range g.Decisions {
							act := "remove"
							if d.Keep {
								act = "keep  "
							}
							fmt.Fprintf(a.out, "  %s %s  %s  %-12s %s\n", act, d.Snapshot.ID().Short(), ui.Time(d.Snapshot.Time), d.Snapshot.Type, strings.Join(d.Reasons, ", "))
						}
					}
				}
				remove = retention.Forget(groups)
			}
			if !dryRun {
				for _, s := range remove {
					if err := repo.RemoveSnapshot(ctx, s.ID()); err != nil {
						lock.Unlock()
						return err
					}
				}
			}
			lock.Unlock()
			verb := "removed"
			if dryRun {
				verb = "would remove"
			}
			var ids []string
			for _, s := range remove {
				ids = append(ids, s.ID().String())
			}
			out := map[string]any{"removed": ids, "dry_run": dryRun}
			if !a.jsonOut {
				a.printf("%s %d snapshots\n", verb, len(remove))
			}
			if prune && !dryRun && len(remove) > 0 {
				st, err := a.eng.Prune(ctx, repo, rc.Maintenance.RepackThreshold, false, func(m string) { a.logf("%s", m) })
				if err != nil {
					return err
				}
				out["prune"] = st
				a.printf("pruned: freed %s\n", ui.Bytes(st.BytesFreed))
			}
			if a.jsonOut {
				return a.printJSON(out)
			}
			return nil
		},
	}
	pf.add(cmd)
	f.add(cmd)
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be removed")
	cmd.Flags().BoolVar(&prune, "prune", false, "prune unreferenced data afterwards")
	return cmd
}

func (a *App) copyCmd() *cobra.Command {
	var from, to string
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "copy --from REPO --to REPO [SNAPSHOT...]",
		Short: "Copy snapshots to another repository (replication / 3-2-1)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if from == "" {
				from = a.repoFlag
			}
			if from == "" || to == "" {
				return errors.New("--from and --to are required")
			}
			src, _, err := a.openRepo(ctx, from)
			if err != nil {
				return err
			}
			defer src.Close()
			dst, _, err := a.openRepo(ctx, to)
			if err != nil {
				return err
			}
			defer dst.Close()
			all, err := src.Snapshots(ctx)
			if err != nil {
				return err
			}
			snaps := repository.FilterSnapshots(all, f.filter())
			if len(args) > 0 {
				snaps = nil
				for _, spec := range args {
					s, err := repository.ResolveSnapshot(all, spec, f.filter())
					if err != nil {
						return err
					}
					snaps = append(snaps, s)
				}
			}
			res, err := engine.Copy(ctx, src, dst, snaps, func(m string) { a.logf("%s", m) })
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(res)
			}
			a.printf("copied %d snapshots (%d already present), %s added\n", res.Copied, res.Skipped, ui.Bytes(res.Bytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source repository (default --repo)")
	cmd.Flags().StringVar(&to, "to", "", "destination repository")
	f.add(cmd)
	return cmd
}

