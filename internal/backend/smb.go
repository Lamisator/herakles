package backend

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/hirochachacha/go-smb2"
)

type smbFS struct{ s *smb2.Share }

func (s smbFS) MkdirAll(p string) error                 { return s.s.MkdirAll(p, 0o700) }
func (s smbFS) Create(p string) (remoteFile, error)     { return s.s.Create(p) }
func (s smbFS) Open(p string) (remoteFile, error)       { return s.s.Open(p) }
func (s smbFS) Stat(p string) (os.FileInfo, error)      { return s.s.Stat(p) }
func (s smbFS) Rename(oldp, newp string) error          { return s.s.Rename(oldp, newp) }
func (s smbFS) Remove(p string) error                   { return s.s.Remove(p) }
func (s smbFS) ReadDir(p string) ([]os.FileInfo, error) { return s.s.ReadDir(p) }

// NewSMB connects to smb://[domain;]user@server[:port]/share/path without
// requiring the share to be mounted.
func NewSMB(raw string, opts Options) (Backend, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	user, domain := opts.Username, opts.Domain
	if u.User != nil && u.User.Username() != "" {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok && opts.Password == "" {
			opts.Password = p
		}
	}
	if d, n, ok := strings.Cut(user, ";"); ok {
		domain, user = d, n
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "445")
	}
	share, root, err := smbPath(u.Path)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("smb connect %s: %w", host, err)
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: opts.Password, Domain: domain}}
	sess, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smb login: %w", err)
	}
	mnt, err := sess.Mount(share)
	if err != nil {
		_ = sess.Logoff()
		return nil, fmt.Errorf("smb mount %s: %w", share, err)
	}
	return newFSBackend(smbFS{mnt}, root, raw, func() error {
		_ = mnt.Umount()
		return sess.Logoff()
	}, 4), nil
}

// smbPath splits the URL path into share and folder. Doubled or trailing
// slashes and backslashes are ignored ("/backup//herakles/" = share
// "backup", folder "herakles").
func smbPath(p string) (share, root string, err error) {
	var parts []string
	for _, s := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s != "." {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "", "", fmt.Errorf("smb URL must contain a share name: smb://server/share/path")
	}
	root = "."
	if len(parts) > 1 {
		root = strings.Join(parts[1:], "/")
	}
	return parts[0], root, nil
}
