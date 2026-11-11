package backend

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// conformance exercises the Backend contract.
func conformance(t *testing.T, b Backend) {
	ctx := context.Background()
	if err := b.Create(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := Handle{Type: ConfigFile}
	if _, err := b.Stat(ctx, cfg); !IsNotFound(err) {
		t.Fatalf("stat missing config: %v", err)
	}
	if err := b.Save(ctx, cfg, []byte("config")); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, cfg, []byte("other")); err == nil {
		t.Fatal("config overwritten")
	}
	data, err := LoadAll(ctx, b, cfg)
	if err != nil || string(data) != "config" {
		t.Fatalf("load config: %q %v", data, err)
	}
	packs := []string{strings.Repeat("ab", 32), strings.Repeat("cd", 32), strings.Repeat("ab", 31) + "ff"}
	for i, p := range packs {
		if err := b.Save(ctx, Handle{Type: PackFile, Name: p}, bytes.Repeat([]byte{byte(i)}, 1000+i)); err != nil {
			t.Fatal(err)
		}
	}
	var listed []string
	if err := b.List(ctx, PackFile, func(fi FileInfo) error {
		listed = append(listed, fi.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(listed)
	sort.Strings(packs)
	if strings.Join(listed, ",") != strings.Join(packs, ",") {
		t.Fatalf("list: %v", listed)
	}
	h := Handle{Type: PackFile, Name: packs[1]}
	fi, err := b.Stat(ctx, h)
	if err != nil || fi.Size < 1000 {
		t.Fatalf("stat: %+v %v", fi, err)
	}
	part, err := LoadRange(ctx, b, h, 10, 20)
	if err != nil || len(part) != 20 {
		t.Fatalf("range: %v", err)
	}
	rd, err := b.Load(ctx, h, 990, 0)
	if err != nil {
		t.Fatal(err)
	}
	tail, _ := io.ReadAll(rd)
	rd.Close()
	if int64(len(tail)) != fi.Size-990 {
		t.Fatalf("tail length %d", len(tail))
	}
	// Lock files may be overwritten.
	lh := Handle{Type: LockFile, Name: "lock1"}
	b.Save(ctx, lh, []byte("a"))
	if err := b.Save(ctx, lh, []byte("bb")); err != nil {
		t.Fatal("lock overwrite:", err)
	}
	if d, _ := LoadAll(ctx, b, lh); string(d) != "bb" {
		t.Fatalf("lock content %q", d)
	}
	if err := b.Remove(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, h); !IsNotFound(err) {
		t.Fatalf("double remove: %v", err)
	}
	if _, err := b.Load(ctx, h, 0, 0); !IsNotFound(err) {
		t.Fatalf("load removed: %v", err)
	}
	if err := b.Save(ctx, Handle{Type: SnapFile, Name: "../evil"}, nil); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocal(t *testing.T) { conformance(t, NewLocal(t.TempDir()+"/repo")) }
func TestMem(t *testing.T)   { conformance(t, NewMem()) }

func TestLimited(t *testing.T) {
	b := NewLimited(NewMem(), 512, 512)
	conformance(t, b)
	// 300 KiB at 512 KiB/s with a 256 KiB burst must take noticeable time.
	start := time.Now()
	b.Save(context.Background(), Handle{Type: SnapFile, Name: "x"}, make([]byte, 600<<10))
	if time.Since(start) < 300*time.Millisecond {
		t.Fatalf("upload limit not applied (%v)", time.Since(start))
	}
}

func TestOpenSchemes(t *testing.T) {
	for _, loc := range []string{"/tmp/x", "file:///tmp/x", "rest:https://h/x", "herakles://h:8443/x", "herakles+http://h/x"} {
		b, err := Open(loc, Options{})
		if err != nil {
			t.Errorf("%s: %v", loc, err)
			continue
		}
		b.Close()
	}
	for _, loc := range []string{"", "ftp://x/y", "rest:ftp://h"} {
		if _, err := Open(loc, Options{}); err == nil {
			t.Errorf("%s accepted", loc)
		}
	}
}

// startSFTP runs an in-process SSH server with an SFTP subsystem.
func startSFTP(t *testing.T) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "backup" && string(pw) == "pw" {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range creqs {
							ok := r.Type == "subsystem" && string(r.Payload[4:]) == "sftp"
							r.Reply(ok, nil)
							if ok {
								srv, _ := sftp.NewServer(ch)
								srv.Serve()
								ch.Close()
							}
						}
					}()
				}
			}(c)
		}
	}()
	return l.Addr().String()
}

func TestSFTP(t *testing.T) {
	addr := startSFTP(t)
	dir := t.TempDir()
	b, err := NewSFTP("sftp://backup@"+addr+dir+"/repo", Options{Password: "pw", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, b)
	if _, err := NewSFTP("sftp://backup@"+addr+dir, Options{Password: "bad", InsecureSkipVerify: true}); err == nil {
		t.Fatal("bad password accepted")
	}
	if _, err := NewSFTP("sftp://backup@"+addr+dir, Options{Password: "pw", KnownHostsFile: dir + "/missing"}); err == nil {
		t.Fatal("unverified host key accepted")
	}
}
