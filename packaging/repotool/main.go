// Command repotool writes the package indexes for the Herakles download
// repositories without distribution tools:
//
//	repotool apt <dir>        dists/stable/… for the .deb files in <dir>/pool
//	repotool pacman <dir>     herakles.db for the .pkg.tar.zst files in <dir>
//
// Signing (InRelease, Release.gpg, *.sig) is done by packaging/build.sh
// with gpg.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: repotool apt|pacman <dir>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "apt":
		err = apt(os.Args[2], time.Now())
	case "pacman":
		err = pacman(os.Args[2])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "repotool:", err)
		os.Exit(1)
	}
}

// sums returns size and hex digests of a file.
func sums(p string) (size int64, md, s1, s256 string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", "", "", err
	}
	defer f.Close()
	hs := []hash.Hash{md5.New(), sha1.New(), sha256.New()}
	size, err = io.Copy(io.MultiWriter(hs[0], hs[1], hs[2]), f)
	return size, hex.EncodeToString(hs[0].Sum(nil)), hex.EncodeToString(hs[1].Sum(nil)), hex.EncodeToString(hs[2].Sum(nil)), err
}

// ---------- apt ----------

// apt writes dists/stable/main/binary-<arch>/Packages(.gz) and
// dists/stable/Release for all .deb files below dir/pool.
func apt(dir string, now time.Time) error {
	debs, err := filepath.Glob(filepath.Join(dir, "pool", "main", "*", "*", "*.deb"))
	if err != nil || len(debs) == 0 {
		return fmt.Errorf("no .deb files in %s/pool/main/*/*/", dir)
	}
	sort.Strings(debs)
	byArch := map[string]*bytes.Buffer{}
	for _, d := range debs {
		ctl, err := debControl(d)
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
		arch := field(ctl, "Architecture")
		if arch == "" {
			return fmt.Errorf("%s: no Architecture", d)
		}
		size, md, s1, s256, err := sums(d)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, d)
		b := byArch[arch]
		if b == nil {
			b = &bytes.Buffer{}
			byArch[arch] = b
		}
		b.WriteString(strings.TrimRight(ctl, "\n"))
		fmt.Fprintf(b, "\nFilename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n\n", filepath.ToSlash(rel), size, md, s1, s256)
	}
	dist := filepath.Join(dir, "dists", "stable")
	var archs, files []string
	for a, b := range byArch {
		archs = append(archs, a)
		sub := filepath.Join("main", "binary-"+a)
		if err := os.MkdirAll(filepath.Join(dist, sub), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dist, sub, "Packages"), b.Bytes(), 0o644); err != nil {
			return err
		}
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		zw.Write(b.Bytes())
		zw.Close()
		if err := os.WriteFile(filepath.Join(dist, sub, "Packages.gz"), gz.Bytes(), 0o644); err != nil {
			return err
		}
		files = append(files, filepath.Join(sub, "Packages"), filepath.Join(sub, "Packages.gz"))
	}
	sort.Strings(archs)
	sort.Strings(files)
	var r strings.Builder
	fmt.Fprintf(&r, "Origin: Herakles\nLabel: Herakles\nSuite: stable\nCodename: stable\nDate: %s\nArchitectures: %s\nComponents: main\nDescription: Herakles backup packages\n",
		now.UTC().Format(time.RFC1123Z), strings.Join(archs, " "))
	type entry struct {
		size         int64
		md, s1, s256 string
		name         string
	}
	var es []entry
	for _, f := range files {
		size, md, s1, s256, err := sums(filepath.Join(dist, f))
		if err != nil {
			return err
		}
		es = append(es, entry{size, md, s1, s256, filepath.ToSlash(f)})
	}
	for _, h := range []struct {
		title string
		get   func(entry) string
	}{{"MD5Sum", func(e entry) string { return e.md }}, {"SHA1", func(e entry) string { return e.s1 }}, {"SHA256", func(e entry) string { return e.s256 }}} {
		r.WriteString(h.title + ":\n")
		for _, e := range es {
			fmt.Fprintf(&r, " %s %16d %s\n", h.get(e), e.size, e.name)
		}
	}
	return os.WriteFile(filepath.Join(dist, "Release"), []byte(r.String()), 0o644)
}

func field(ctl, name string) string {
	for _, l := range strings.Split(ctl, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && k == name {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// debControl returns the control file of a .deb (an ar archive holding
// control.tar.{gz,xz,zst}).
func debControl(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != "!<arch>\n" {
		return "", errors.New("not a .deb (ar) file")
	}
	hdr := make([]byte, 60)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return "", errors.New("no control archive")
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[:16])), "/")
		var size int64
		fmt.Sscan(strings.TrimSpace(string(hdr[48:58])), &size)
		data := io.LimitReader(br, size)
		if strings.HasPrefix(name, "control.tar") {
			var r io.Reader
			switch {
			case strings.HasSuffix(name, ".gz"):
				zr, err := gzip.NewReader(data)
				if err != nil {
					return "", err
				}
				r = zr
			case strings.HasSuffix(name, ".zst"):
				zr, err := zstd.NewReader(data)
				if err != nil {
					return "", err
				}
				defer zr.Close()
				r = zr
			case name == "control.tar":
				r = data
			default:
				return "", fmt.Errorf("unsupported %s", name)
			}
			b, err := tarFile(r, "control")
			return string(b), err
		}
		if _, err := io.Copy(io.Discard, data); err != nil {
			return "", err
		}
		if size%2 == 1 {
			br.ReadByte()
		}
	}
}

// tarFile returns the content of the member called name (with or without
// a leading ./).
func tarFile(r io.Reader, name string) ([]byte, error) {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s not found", name)
		}
		if strings.TrimPrefix(h.Name, "./") == name {
			return io.ReadAll(tr)
		}
	}
}

// ---------- pacman ----------

// pacman writes herakles.db (+ .db.tar.gz) listing the packages in dir, the
// format repo-add produces: a tar.gz with <name>-<version>/desc per package.
func pacman(dir string) error {
	pkgs, err := filepath.Glob(filepath.Join(dir, "*.pkg.tar.zst"))
	if err != nil || len(pkgs) == 0 {
		return fmt.Errorf("no .pkg.tar.zst files in %s", dir)
	}
	sort.Strings(pkgs)
	var out bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	seen := map[string]bool{}
	for _, p := range pkgs {
		info, err := pkgInfo(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		one := func(k string) string {
			if v := info[k]; len(v) > 0 {
				return v[0]
			}
			return ""
		}
		name, ver := one("pkgname"), one("pkgver")
		if name == "" || ver == "" {
			return fmt.Errorf("%s: incomplete .PKGINFO", p)
		}
		if seen[name] {
			return fmt.Errorf("%s: two packages named %s in one repository", p, name)
		}
		seen[name] = true
		size, md, _, s256, err := sums(p)
		if err != nil {
			return err
		}
		var d strings.Builder
		add := func(k string, vs ...string) {
			var keep []string
			for _, v := range vs {
				if v != "" {
					keep = append(keep, v)
				}
			}
			if len(keep) > 0 {
				fmt.Fprintf(&d, "%%%s%%\n%s\n\n", k, strings.Join(keep, "\n"))
			}
		}
		add("FILENAME", filepath.Base(p))
		add("NAME", name)
		add("BASE", one("pkgbase"))
		add("VERSION", ver)
		add("DESC", one("pkgdesc"))
		add("CSIZE", fmt.Sprint(size))
		add("ISIZE", one("size"))
		add("MD5SUM", md)
		add("SHA256SUM", s256)
		if sig, err := os.ReadFile(p + ".sig"); err == nil {
			add("PGPSIG", base64.StdEncoding.EncodeToString(sig))
		}
		add("URL", one("url"))
		add("LICENSE", info["license"]...)
		add("ARCH", one("arch"))
		add("BUILDDATE", one("builddate"))
		add("PACKAGER", one("packager"))
		add("REPLACES", info["replaces"]...)
		add("CONFLICTS", info["conflict"]...)
		add("PROVIDES", info["provides"]...)
		add("DEPENDS", info["depend"]...)
		add("OPTDEPENDS", info["optdepend"]...)
		dirName := name + "-" + ver
		now := time.Now()
		tw.WriteHeader(&tar.Header{Name: dirName + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: now})
		tw.WriteHeader(&tar.Header{Name: dirName + "/desc", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(d.Len()), ModTime: now})
		tw.Write([]byte(d.String()))
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	for _, n := range []string{"herakles.db.tar.gz", "herakles.db"} {
		if err := os.WriteFile(filepath.Join(dir, n), out.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pkgInfo reads .PKGINFO (key = value lines) from a zstd-compressed package.
func pkgInfo(p string) (map[string][]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	b, err := tarFile(zr, ".PKGINFO")
	if err != nil {
		return nil, err
	}
	info := map[string][]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok && !strings.HasPrefix(l, "#") {
			info[k] = append(info[k], v)
		}
	}
	return info, nil
}
