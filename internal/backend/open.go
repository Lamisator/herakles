package backend

import (
	"fmt"
	"net/url"
	"runtime"
	"strings"
)

// Options carry backend specific connection settings.
type Options struct {
	// Username and Password are used for the repository server (HTTP basic
	// auth), SFTP password authentication and SMB (NTLM) authentication.
	Username string
	Password string
	// Domain is the SMB/NTLM domain.
	Domain string
	// IdentityFile is an SSH private key for SFTP.
	IdentityFile string
	// KnownHostsFile verifies SFTP host keys (default ~/.ssh/known_hosts).
	KnownHostsFile string
	// CACertFile is a PEM CA bundle for the repository server.
	CACertFile string
	// InsecureSkipVerify disables TLS / SSH host key verification.
	InsecureSkipVerify bool
	// UploadLimit and DownloadLimit throttle traffic in KiB/s (0 = unlimited).
	UploadLimit   int
	DownloadLimit int
}

// Open creates a backend for a repository URL. Supported forms:
//
//	/path/to/repo, C:\repo, \\server\share\repo, file:///path   local / mounted share
//	sftp://user@host[:port]/path                                SFTP
//	smb://[domain;]user@server[:port]/share/path                SMB/CIFS share
//	rest:https://host:8443/name  or  herakles://host:8443/name  Herakles server
func Open(location string, opts Options) (Backend, error) {
	var b Backend
	var err error
	lower := strings.ToLower(location)
	switch {
	case location == "":
		return nil, fmt.Errorf("empty repository location")
	case strings.HasPrefix(lower, "rest:"):
		b, err = NewREST(location[5:], opts)
	case strings.HasPrefix(lower, "herakles://"):
		b, err = NewREST("https://"+location[len("herakles://"):], opts)
	case strings.HasPrefix(lower, "herakles+http://"):
		b, err = NewREST("http://"+location[len("herakles+http://"):], opts)
	case strings.HasPrefix(lower, "sftp://"):
		b, err = NewSFTP(location, opts)
	case strings.HasPrefix(lower, "smb://"):
		b, err = NewSMB(location, opts)
	case strings.HasPrefix(lower, "file://"):
		u, perr := url.Parse(location)
		if perr != nil {
			return nil, perr
		}
		p := u.Path
		if runtime.GOOS == "windows" {
			p = strings.TrimPrefix(p, "/")
		}
		b, err = openLocal(p)
	default:
		if i := strings.Index(location, "://"); i > 0 && !isWindowsDrive(location) {
			return nil, fmt.Errorf("unsupported repository URL scheme %q", location[:i])
		}
		b, err = openLocal(location)
	}
	if err != nil {
		return nil, err
	}
	if opts.UploadLimit > 0 || opts.DownloadLimit > 0 {
		b = NewLimited(b, opts.UploadLimit, opts.DownloadLimit)
	}
	return b, nil
}

// openLocal opens a repository folder, or backup files copied out of one.
func openLocal(p string) (Backend, error) {
	if IsLoose(p) {
		return NewLoose(p)
	}
	return NewLocal(p), nil
}

func isWindowsDrive(s string) bool {
	return len(s) >= 2 && s[1] == ':' && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z'))
}
