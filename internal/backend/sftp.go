package backend

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sftpFS struct{ c *sftp.Client }

func (s sftpFS) MkdirAll(p string) error                 { return s.c.MkdirAll(p) }
func (s sftpFS) Create(p string) (remoteFile, error)     { return s.c.Create(p) }
func (s sftpFS) Open(p string) (remoteFile, error)       { return s.c.Open(p) }
func (s sftpFS) Stat(p string) (os.FileInfo, error)      { return s.c.Stat(p) }
func (s sftpFS) Remove(p string) error                   { return s.c.Remove(p) }
func (s sftpFS) ReadDir(p string) ([]os.FileInfo, error) { return s.c.ReadDir(p) }
func (s sftpFS) Rename(oldp, newp string) error {
	if err := s.c.PosixRename(oldp, newp); err == nil {
		return nil
	}
	return s.c.Rename(oldp, newp)
}

// NewSFTP connects to sftp://user@host[:port]/path. Authentication uses, in
// order: the configured identity file, a running ssh-agent, default keys in
// ~/.ssh and the configured password.
func NewSFTP(raw string, opts Options) (Backend, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	user := opts.Username
	if u.User != nil && u.User.Username() != "" {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok && opts.Password == "" {
			opts.Password = p
		}
	}
	if user == "" {
		user = os.Getenv("USER")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "22")
	}
	var auths []ssh.AuthMethod
	var signers []ssh.Signer
	home, _ := os.UserHomeDir()
	keyFiles := []string{}
	if opts.IdentityFile != "" {
		keyFiles = append(keyFiles, opts.IdentityFile)
	} else if home != "" {
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			keyFiles = append(keyFiles, filepath.Join(home, ".ssh", n))
		}
	}
	for _, kf := range keyFiles {
		data, err := os.ReadFile(kf)
		if err != nil {
			if opts.IdentityFile != "" {
				return nil, fmt.Errorf("read identity file: %w", err)
			}
			continue
		}
		s, err := ssh.ParsePrivateKey(data)
		if err != nil {
			if opts.IdentityFile != "" {
				return nil, fmt.Errorf("parse identity file: %w", err)
			}
			continue
		}
		signers = append(signers, s)
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			if as, err := agent.NewClient(conn).Signers(); err == nil {
				signers = append(signers, as...)
			}
		}
	}
	if len(signers) > 0 {
		auths = append(auths, ssh.PublicKeys(signers...))
	}
	if opts.Password != "" {
		auths = append(auths, ssh.Password(opts.Password))
	}
	var hostKeyCB ssh.HostKeyCallback
	if opts.InsecureSkipVerify {
		hostKeyCB = ssh.InsecureIgnoreHostKey() //nolint:gosec // explicit opt-in
	} else {
		kh := opts.KnownHostsFile
		if kh == "" && home != "" {
			kh = filepath.Join(home, ".ssh", "known_hosts")
		}
		cb, err := knownhosts.New(kh)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts %s: %w (set insecure_skip_verify to bypass)", kh, err)
		}
		hostKeyCB = cb
	}
	client, err := ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hostKeyCB,
		Timeout:         30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", host, err)
	}
	sc, err := sftp.NewClient(client, sftp.UseConcurrentWrites(true))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sftp: %w", err)
	}
	root := u.Path
	if strings.HasPrefix(root, "/~/") {
		root = root[3:]
	}
	if root == "" {
		root = "."
	}
	return newFSBackend(sftpFS{sc}, root, raw, func() error {
		sc.Close()
		return client.Close()
	}, 8), nil
}
