package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/blockdev"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) devicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List disks and partitions that can be backed up as images",
		Long: `List disks, partitions and volumes (LVM, LUKS, RAID) for device backups
(the "devices" list of a job). Use the STABLE path in jobs: unlike
/dev/sdb1 it does not change when disks are added or reordered.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			devs, err := blockdev.List()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(devs)
			}
			depth := map[string]int{}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "DEVICE\tSIZE\tDESCRIPTION\tSTABLE PATH")
			for _, d := range devs {
				n := 0
				if d.Parent != "" {
					n = depth[d.Parent] + 1
				}
				depth[d.Name] = n
				stable := d.Stable
				if stable == "" || stable == d.Path {
					stable = "-"
				}
				fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", strings.Repeat("  ", n), d.Path, ui.Bytes(int64(d.Size)), d.Describe(), stable)
			}
			return tw.Flush()
		},
	}
}
