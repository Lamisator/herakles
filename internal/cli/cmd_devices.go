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
			ct := blockdev.Container()
			blocked := 0
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
				desc := d.Describe()
				if d.Unavailable != "" {
					desc = "unavailable: " + d.Unavailable + " · " + desc
					blocked++
				}
				fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", strings.Repeat("  ", n), d.Path, ui.Bytes(int64(d.Size)), desc, stable)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if ct != "" && blocked > 0 {
				a.printf("\n%s\n", containerHint(ct))
			}
			return nil
		},
	}
}

// containerHint explains why disks cannot be imaged from a container and
// what to do instead.
func containerHint(ct string) string {
	return fmt.Sprintf(`Herakles runs in a container (%s). It sees the host's disks but cannot read
them: the host does not pass them in. Instead:
  - back up the whole container as files: a job with paths: [/]
    (the wizard's "This whole container"); /proc, /sys, /dev and /run
    are left out by themselves;
  - or image the container's volume on the host, with Herakles running there
    (e.g. /dev/pve/vm-101-disk-0 on Proxmox);
  - or pass a disk into the container (Proxmox: dev0: /dev/sdb) to image it.`, ct)
}
