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
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	if parts[0] == "" {
		return nil, fmt.Errorf("smb URL must contain a share name: smb://server/share/path")
	}
	root := "."
	if len(parts) == 2 && parts[1] != "" {
		root = strings.TrimSuffix(parts[1], "/")
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
	share, err := sess.Mount(parts[0])
	if err != nil {
		_ = sess.Logoff()
		return nil, fmt.Errorf("smb mount %s: %w", parts[0], err)
	}
	return newFSBackend(smbFS{share}, root, raw, func() error {
		_ = share.Umount()
		return sess.Logoff()
	}, 4), nil
}
