package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/dbdump"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) databasesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "databases",
		Aliases: []string{"db"},
		Short:   "Back up and restore databases (Postgres, MySQL/MariaDB, MongoDB, SQLite)",
		Long: `Jobs with a databases section dump each database with its own tools
(pg_dump, mysqldump or mariadb-dump, mongodump, sqlite3) and store the dumps
in the job's snapshot under /herakles-databases/<name>/, streamed without
temporary files.

  jobs:
    - name: db
      repository: nas
      databases:
        - type: postgres        # local server: dumped as the user postgres
        - type: mysql           # MariaDB too; local root over the socket
        - type: sqlite
          databases: [/var/lib/app/*.db]

'herakles databases ls' shows what would be dumped and checks the
connections; 'herakles databases restore' loads a dump back into the server.`,
	}
	cmd.AddCommand(a.databasesLsCmd(), a.databasesRestoreCmd())
	return cmd
}

func (a *App) databasesLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls [JOB]",
		Short: "Show the databases each job dumps (connects to the servers)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			type entry struct {
				Job      string        `json:"job"`
				Name     string        `json:"name"`
				Type     string        `json:"type"`
				Dumps    []dbdump.Dump `json:"dumps,omitempty"`
				Error    string        `json:"error,omitempty"`
				Snapshot []string      `json:"paths,omitempty"`
			}
			var out []entry
			found := false
			for _, j := range a.cfg.Jobs {
				if len(args) == 1 && j.Name != args[0] {
					continue
				}
				for _, d := range j.Databases {
					found = true
					e := entry{Job: j.Name, Name: d.Label(), Type: d.Type}
					dumps, err := dbdump.Plan(cmd.Context(), d)
					if err != nil {
						e.Error = err.Error()
					}
					e.Dumps = dumps
					out = append(out, e)
				}
			}
			if a.jsonOut {
				return a.printJSON(out)
			}
			if !found {
				if len(args) == 1 && a.cfg.Job(args[0]) == nil {
					return fmt.Errorf("unknown job %q", args[0])
				}
				a.printf("no job has a databases section (see 'herakles databases --help')\n")
				return nil
			}
			for _, e := range out {
				a.printf("%s: %s (%s)\n", e.Job, e.Name, e.Type)
				if e.Error != "" {
					a.printf("  error: %s\n", e.Error)
					continue
				}
				for _, d := range e.Dumps {
					what := d.Database
					if what == "" && d.File == dbdump.GlobalsFile {
						what = "roles and tablespaces"
					} else if what == "" {
						what = "all databases"
					}
					size := ""
					if d.Size > 0 {
						size = "  " + ui.Bytes(d.Size)
					}
					a.printf("  %-30s -> %s%s\n", what, path.Join("/", repository.DatabasesDir, e.Name, d.File), size)
				}
			}
			return nil
		},
	}
}

func (a *App) databasesRestoreCmd() *cobra.Command {
	var into string
	var replace, dryRun bool
	var f snapFilterFlags
	cmd := &cobra.Command{
		Use:   "restore SNAPSHOT [NAME/DATABASE]",
		Short: "Load a database dump from a snapshot back into its server",
		Long: `Load a dump into the database server it came from, using the connection
settings of the job's databases entry. Without a database, list the dumps
in the snapshot.

Postgres: the database is created if it does not exist; the dump replaces
its tables (all or nothing). On a new server, restore NAME/_globals first:
it creates the roles that own the tables.
MySQL/MariaDB: the database is created if needed and the dump replaces
its tables.
MongoDB: collections in the dump replace the existing ones.
SQLite: the file is written again; an existing file is only replaced with
--replace.

To get the dump file itself, use 'herakles dump SNAPSHOT /herakles-databases/...'.`,
		Example: `  herakles databases restore latest --job db
  herakles databases restore latest postgres/app --job db
  herakles databases restore 1a2b3c4d postgres/app --into app_restored
  herakles databases restore latest sqlite/app --into /tmp/app.db`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, _, err := a.openRepo(ctx, a.jobRepo(f.job))
			if err != nil {
				return err
			}
			defer repo.Close()
			snap, err := findDatabaseSnapshot(ctx, repo, args[0], f.filter())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if a.jsonOut {
					return a.printJSON(snap.Databases)
				}
				a.printf("snapshot %s (%s, %s):\n", snap.ID().Short(), snap.Job, snap.Time.Local().Format("2006-01-02 15:04"))
				for _, r := range snap.Databases {
					state := ui.Bytes(r.Size)
					if r.Error != "" {
						state = "failed: " + firstLine(r.Error)
					}
					a.printf("  %-30s %-9s %s\n", dumpKey(r), r.Kind, state)
				}
				return nil
			}
			rec, err := findDump(snap.Databases, args[1])
			if err != nil {
				return err
			}
			if rec.Error != "" || rec.Path == "" {
				return fmt.Errorf("%s was not dumped in this snapshot: %s", dumpKey(*rec), rec.Error)
			}
			target := into
			if target == "" {
				target = rec.Database
			}
			if target == "" {
				target = "the server"
			}
			if dryRun {
				a.printf("would load %s (%s) into %s (%s)\n", rec.Path, ui.Bytes(rec.Size), target, rec.Kind)
				return nil
			}
			if err := a.eng.RestoreDatabase(ctx, repo, snap, rec.Path, dbdump.RestoreOptions{Into: into, Replace: replace}); err != nil {
				return err
			}
			a.printf("restored %s into %s\n", dumpKey(*rec), target)
			return nil
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "target database (SQLite: file) instead of the original")
	cmd.Flags().BoolVar(&replace, "replace", false, "SQLite: replace an existing file")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be restored")
	f.add(cmd)
	return cmd
}

// findDatabaseSnapshot resolves a snapshot; "latest" without a job means
// the latest snapshot that holds database dumps.
func findDatabaseSnapshot(ctx context.Context, repo *repository.Repository, spec string, f repository.SnapshotFilter) (*repository.Snapshot, error) {
	if spec == "latest" && f.Job == "" {
		all, err := repo.Snapshots(ctx)
		if err != nil {
			return nil, err
		}
		snaps := repository.FilterSnapshots(all, f)
		repository.SortSnapshots(snaps)
		for i := len(snaps) - 1; i >= 0; i-- {
			if len(snaps[i].Databases) > 0 {
				return snaps[i], nil
			}
		}
		return nil, errors.New("no snapshot with database dumps")
	}
	snap, err := repo.FindSnapshot(ctx, spec, f)
	if err != nil {
		return nil, err
	}
	if len(snap.Databases) == 0 {
		return nil, fmt.Errorf("snapshot %s holds no database dumps", snap.ID().Short())
	}
	return snap, nil
}

// dumpKey names a dump as NAME/DATABASE (NAME/_globals for Postgres roles).
func dumpKey(r repository.DatabaseDump) string {
	if r.Path == "" {
		return r.Source
	}
	base := path.Base(r.Path)
	return r.Source + "/" + strings.TrimSuffix(base, path.Ext(base))
}

// findDump matches NAME/DATABASE, a database name or a dump name.
func findDump(recs []repository.DatabaseDump, want string) (*repository.DatabaseDump, error) {
	var hits []int
	for i, r := range recs {
		if dumpKey(r) == want || r.Source+"/"+r.Database == want {
			return &recs[i], nil
		}
		if r.Database == want || (r.Path != "" && strings.TrimSuffix(path.Base(r.Path), path.Ext(r.Path)) == want) || (r.Source == want && len(recs) == 1) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return &recs[hits[0]], nil
	case 0:
		return nil, fmt.Errorf("no dump %q in this snapshot (list them by leaving out the database)", want)
	}
	return nil, fmt.Errorf("%q is ambiguous; use NAME/DATABASE", want)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
