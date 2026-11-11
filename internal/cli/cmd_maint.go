package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) pruneCmd() *cobra.Command {
	var dryRun bool
	var threshold float64
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove data not referenced by any snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, rc, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			if threshold == 0 {
				threshold = rc.Maintenance.RepackThreshold
			}
			st, err := a.eng.Prune(cmd.Context(), repo, threshold, dryRun, func(m string) { a.logf("%s", m) })
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(st)
			}
			verb := "freed"
			if dryRun {
				verb = "would free"
			}
			a.printf("%s %s: %d packs deleted, %d repacked (%s rewritten), %d unreferenced packs, %d unused blobs\n",
				verb, ui.Bytes(st.BytesFreed), st.PacksDeleted, st.PacksRepacked, ui.Bytes(st.BytesRepacked), st.UnreferencedPacks, st.BlobsRemoved)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be done")
	cmd.Flags().Float64Var(&threshold, "repack-threshold", 0, "repack packs with more than this fraction unused (default 0.2)")
	return cmd
}

func (a *App) checkCmd() *cobra.Command {
	var readData string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify repository integrity",
		Long: `Check the repository structure: index, packs, snapshots and trees. With
--read-data all (or a percentage of) the data is downloaded, decrypted and
verified.`,
		Example: "  herakles check\n  herakles check --read-data 10%\n  herakles check --read-data 100%",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			frac, err := config.ParsePercent(readData)
			if err != nil {
				return err
			}
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			res, err := a.eng.Check(cmd.Context(), repo, frac, func(m string) {
				if a.verbose {
					a.logf("%s", m)
				}
			})
			if err != nil {
				return err
			}
			if a.jsonOut {
				_ = a.printJSON(res)
			} else {
				for _, w := range res.Warnings {
					a.printf("warning: %s\n", w)
				}
				for _, e := range res.Errors {
					fmt.Fprintf(a.err, "ERROR: %s\n", e)
				}
				a.printf("checked %d snapshots, %d trees, %d packs", res.Snapshots, res.Trees, res.Packs)
				if res.PacksRead > 0 {
					a.printf(", read %d packs (%s, %d blobs)", res.PacksRead, ui.Bytes(res.BytesRead), res.BlobsRead)
				}
				a.printf("\n")
			}
			if !res.OK() {
				return exitError{1, fmt.Errorf("repository has %d errors", len(res.Errors))}
			}
			a.printf("no errors found\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&readData, "read-data", "", "also verify data: percentage (10%) or fraction (0.1); 100% reads everything")
	return cmd
}

func (a *App) statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats [SNAPSHOT]",
		Short: "Show repository or snapshot statistics",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, _, err := a.openRepo(ctx, "")
			if err != nil {
				return err
			}
			defer repo.Close()
			if len(args) == 1 {
				snap, err := repo.FindSnapshot(ctx, args[0], repository.SnapshotFilter{})
				if err != nil {
					return err
				}
				var files, dirs, size int64
				uniq := map[string]bool{}
				var uniqSize int64
				err = repo.Walk(ctx, snap.Tree, func(p string, n *repository.Node) error {
					if n.Type == repository.NodeDir {
						dirs++
						return nil
					}
					files++
					size += int64(n.Size)
					for _, c := range n.Content {
						if !uniq[c.String()] {
							uniq[c.String()] = true
							s, _ := repo.BlobSize(c)
							uniqSize += s
						}
					}
					return nil
				})
				if err != nil {
					return err
				}
				out := map[string]any{"snapshot": snap.ID().String(), "files": files, "dirs": dirs, "restore_size": size, "unique_data": uniqSize, "chunks": len(uniq)}
				if a.jsonOut {
					return a.printJSON(out)
				}
				a.printf("snapshot %s (%s, %s)\n  files: %d, directories: %d\n  restore size: %s\n  unique chunks: %d (%s)\n",
					snap.ID().Short(), ui.Time(snap.Time), snap.Type, files, dirs, ui.Bytes(size), len(uniq), ui.Bytes(uniqSize))
				return nil
			}
			st, err := repo.Stats(ctx)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(st)
			}
			// Logical size: sum of all snapshot sizes.
			snaps, _ := repo.Snapshots(ctx)
			var logical int64
			for _, s := range snaps {
				if s.Summary != nil {
					logical += s.Summary.TotalBytes
				}
			}
			a.printf("repository %s at %s\n", repo.Config().ID[:8], repo.Backend().Location())
			a.printf("  snapshots:        %d\n", st.Snapshots)
			a.printf("  packs:            %d\n", st.Packs)
			a.printf("  blobs:            %d (%d data, %d tree)\n", st.Blobs, st.DataBlobs, st.TreeBlobs)
			a.printf("  unique data:      %s\n", ui.Bytes(st.RawBytes))
			a.printf("  stored on disk:   %s (compression %s)\n", ui.Bytes(st.StoredBytes), ui.Ratio(st.RawBytes, st.StoredBytes))
			a.printf("  all snapshots:    %s (total space saving %s)\n", ui.Bytes(logical), ui.Ratio(logical, st.StoredBytes))
			return nil
		},
	}
}

func (a *App) keyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage repository keys (passwords and key files)"}
	var newPwFile, newKeyFile, label string
	var noPassword bool
	newCreds := func() (crypto.Credentials, error) {
		var c crypto.Credentials
		if newKeyFile != "" {
			d, err := crypto.LoadKeyFile(newKeyFile)
			if err != nil {
				return c, err
			}
			c.KeyFileData = d
		}
		switch {
		case newPwFile != "":
			rc := &config.Repository{PasswordFile: newPwFile}
			x, err := rc.Credentials()
			if err != nil {
				return c, err
			}
			c.Password = x.Password
		case !noPassword:
			pw, err := a.readPassword("New password: ")
			if err != nil {
				return c, err
			}
			pw2, err := a.readPassword("Repeat new password: ")
			if err != nil {
				return c, err
			}
			if pw != pw2 {
				return c, errors.New("passwords do not match")
			}
			c.Password = pw
		}
		if c.Empty() {
			return c, errors.New("the new key needs a password and/or key file")
		}
		return c, nil
	}
	addFlags := func(c *cobra.Command) {
		c.Flags().StringVar(&newPwFile, "new-password-file", "", "read the new password from a file")
		c.Flags().StringVar(&newKeyFile, "new-key-file", "", "require this key file for the new key")
		c.Flags().BoolVar(&noPassword, "no-password", false, "new key uses only the key file")
		c.Flags().StringVar(&label, "label", "", "description of the new key")
	}
	list := &cobra.Command{
		Use: "list", Short: "List keys", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			keys, err := repo.ListKeys(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(keys)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, " ID\tTYPE\tCREATED\tUSER\tHOST\tLABEL")
			for _, k := range keys {
				cur := " "
				if k.Current {
					cur = "*"
				}
				fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\n", cur, k.Name[:12], k.Kind, ui.Time(k.Created), k.Username, k.Hostname, k.Label)
			}
			return tw.Flush()
		},
	}
	add := &cobra.Command{
		Use: "add", Short: "Add a password/key file that can unlock the repository", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			c, err := newCreds()
			if err != nil {
				return err
			}
			name, err := repo.AddKey(cmd.Context(), c, label)
			if err != nil {
				return err
			}
			a.printf("added key %s\n", name[:12])
			return nil
		},
	}
	addFlags(add)
	passwd := &cobra.Command{
		Use: "passwd", Short: "Change the credentials of the key in use", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			c, err := newCreds()
			if err != nil {
				return err
			}
			if err := repo.ChangeKey(cmd.Context(), c); err != nil {
				return err
			}
			a.printf("key changed; update your configuration if it stores the old password\n")
			return nil
		},
	}
	addFlags(passwd)
	remove := &cobra.Command{
		Use: "remove KEY-ID", Short: "Remove a key", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			if err := repo.RemoveKey(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.printf("removed key %s\n", args[0])
			return nil
		},
	}
	generate := &cobra.Command{
		Use: "generate PATH", Short: "Generate a new random key file", Args: cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := crypto.GenerateKeyFile(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "generated key file %s (keep a copy somewhere safe!)\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(list, add, passwd, remove, generate)
	return cmd
}

func (a *App) unlockCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Remove stale (or all) locks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			n, err := repo.RemoveLocks(cmd.Context(), all)
			if err != nil {
				return err
			}
			a.printf("removed %d locks\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "remove-all", false, "remove all locks, even active ones (dangerous)")
	return cmd
}

func (a *App) repairCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "repair", Short: "Repair repository structures"}
	cmd.AddCommand(&cobra.Command{
		Use:   "index",
		Short: "Rebuild the index from pack headers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, _, err := a.openRepo(cmd.Context(), "")
			if err != nil {
				return err
			}
			defer repo.Close()
			lock, err := repo.Lock(cmd.Context(), true, "repair index")
			if err != nil {
				return err
			}
			defer lock.Unlock()
			n, err := repo.RebuildIndex(cmd.Context(), func(m string) { a.logf("%s", m) })
			if err != nil {
				return err
			}
			a.printf("index rebuilt from %d packs\n", n)
			return nil
		},
	})
	return cmd
}
