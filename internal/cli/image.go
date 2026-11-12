package cli

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/lamisator/herakles/internal/imagefs"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/ui"
)

// imageAt opens the disk image that path p of a snapshot leads into; nil
// when p does not lead into one.
func imageAt(ctx context.Context, repo *repository.Repository, snap *repository.Snapshot, p string) (*restorer.Image, string, error) {
	dev, devPath, inner, ok, err := repo.FindImage(ctx, snap.Tree, p)
	if !ok || err != nil {
		return nil, "", err
	}
	img, err := restorer.OpenImage(ctx, repo, dev, devPath)
	return img, inner, err
}

// lsImage lists the partitions of an image or a folder inside it.
func (a *App) lsImage(img *restorer.Image, p, inner string, long, recursive bool) error {
	base := "/" + strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/")
	vol, fsPath, err := img.Resolve(inner)
	if err != nil {
		return err
	}
	if vol == nil {
		if a.jsonOut {
			return a.printJSON(img.Volumes)
		}
		for _, v := range img.Volumes {
			line := fmt.Sprintf("%s/%s  %s  %s", base, v.Name, ui.Bytes(v.Size), restorer.Describe(v))
			if v.Note != "" {
				line += "  (" + v.Note + ")"
			}
			fmt.Fprintln(a.out, line)
		}
		return nil
	}
	if !vol.Browsable() {
		return fmt.Errorf("%s: %s", restorer.Describe(vol), vol.Note)
	}
	type entry struct {
		Path  string        `json:"path"`
		Entry imagefs.Entry `json:"entry"`
	}
	var out []entry
	var walk func(dir, at string) error
	walk = func(dir, at string) error {
		ents, err := vol.FS.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			p := at + "/" + e.Name
			switch {
			case a.jsonOut:
				out = append(out, entry{p, e})
			case long:
				size := fmt.Sprint(e.Size)
				if e.IsDir() {
					size = "-"
				}
				extra := ""
				if e.Link != "" {
					extra = " -> " + e.Link
				}
				fmt.Fprintf(a.out, "%s %12s %s %s%s\n", e.Mode, size, ui.Time(e.ModTime), p, extra)
			default:
				fmt.Fprintln(a.out, p)
			}
			if recursive && e.IsDir() {
				if err := walk(path.Join(dir, e.Name), p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(fsPath, strings.TrimSuffix(base, "/")); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(out)
	}
	return nil
}

// dumpImage writes a file from an image, or a folder as tar.
func dumpImage(ctx context.Context, img *restorer.Image, inner string, asTar bool, w io.Writer) error {
	src, err := img.Source(inner)
	if err != nil {
		return err
	}
	if asTar || src.Entry.IsDir() {
		return restorer.WriteArchive(ctx, nil, []restorer.Item{{Name: src.Entry.Name, Image: &src}}, restorer.FormatTar, w)
	}
	r, size, err := src.FS.Open(src.Path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.NewSectionReader(r, 0, size))
	return err
}
