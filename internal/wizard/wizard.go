// Package wizard turns the answers of the backup wizard (TUI) into a
// repository and a job: presets for exclusions, schedules and retention.
package wizard

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/retention"
)

// Kinds of backups.
const (
	Files  = "files"
	Docker = "docker"
	Disk   = "disk"
)

// Storage types of a new repository.
const (
	Local  = "local"
	SMB    = "smb"
	SFTP   = "sftp"
	Server = "server"
)

// Password modes of a new repository.
const (
	Generate = "generate" // generated, stored in a file
	Choose   = "choose"   // chosen, stored in a file
	Ask      = "ask"      // not stored
)

// Schedules and retention presets, with descriptions for the menus.
var (
	Schedules = []Preset{{"nightly", "Every night (full every 7 days)"}, {"weekly", "Weekly full, daily incremental"},
		{"hourly", "Every 4 hours"}, {"manual", "Only when started"}}
	Retentions = []Preset{{"standard", "Standard: 7 daily, 4 weekly, 12 monthly"}, {"week", "One week: 7 daily"},
		{"long", "Long: 14 daily, 8 weekly, 24 monthly, 5 yearly"}, {"all", "Keep everything"}}
)

// Preset is a named choice.
type Preset struct{ ID, Label string }

// NewRepo describes a repository to create.
type NewRepo struct {
	Name       string
	Type       string
	Path       string // local
	Host       string // smb, sftp
	Share      string // smb
	Dir        string // smb (folder in the share), sftp (folder on the server)
	User       string
	Pass       string // smb share / server account
	Identity   string // sftp
	URL        string // server host:port
	ServerRepo string // server
	CA         string // server
}

// Plan holds the answers.
type Plan struct {
	Kind          string
	Paths         []string
	SkipCaches    bool
	SkipBuild     bool
	SkipDownloads bool
	OneFS         bool
	Extra         []string
	Docker        *config.DockerJob
	Device        string

	Repo         string   // existing repository, or "" with New
	New          *NewRepo // repository to create
	PasswordMode string
	Password     string
	Maintenance  bool

	Schedule string
	Time     string // HH:MM
	Keep     string

	Name, Description string
}

// GeneratePassword returns a random password like "AbCdEf-…" (24 letters
// and digits without look-alikes).
func GeneratePassword() string {
	const abc = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	var b strings.Builder
	for i := 0; i < 24; i++ {
		if i > 0 && i%6 == 0 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		b.WriteByte(abc[n.Int64()])
	}
	return b.String()
}

// DefaultName suggests a job name that is not taken.
func DefaultName(kind string, cfg *config.Config) string {
	base := map[string]string{Files: "files", Docker: "containers", Disk: "disk"}[kind]
	if base == "" {
		base = "backup"
	}
	n := base
	for i := 2; cfg.Job(n) != nil; i++ {
		n = fmt.Sprintf("%s-%d", base, i)
	}
	return n
}

// DefaultRepoName suggests a repository name that is not taken.
func DefaultRepoName(typ string, cfg *config.Config) string {
	base := map[string]string{Local: "local", SMB: "nas", SFTP: "sftp", Server: "backup-server"}[typ]
	if base == "" {
		base = "repo"
	}
	n := base
	for i := 2; cfg.Repo(n) != nil; i++ {
		n = fmt.Sprintf("%s-%d", base, i)
	}
	return n
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Repository builds the new repository (without its password source).
func (p *Plan) Repository() (*config.Repository, error) {
	n := p.New
	if n == nil {
		return nil, errors.New("no new repository")
	}
	if !validName.MatchString(n.Name) {
		return nil, errors.New("repository names are letters, digits and . _ -")
	}
	r := &config.Repository{Name: n.Name}
	switch n.Type {
	case Local:
		if strings.TrimSpace(n.Path) == "" {
			return nil, errors.New("enter the folder for the backups")
		}
		r.Location = strings.TrimSpace(n.Path)
	case SMB:
		if n.Host == "" || n.Share == "" {
			return nil, errors.New("enter the NAS and the share")
		}
		user := ""
		if n.User != "" {
			user = url.PathEscape(n.User) + "@"
		}
		r.Location = "smb://" + user + strings.TrimSpace(n.Host) + "/" + strings.Trim(n.Share, "/") + joinDir(n.Dir)
		r.Remote.Password = n.Pass
	case SFTP:
		if n.Host == "" || n.Dir == "" {
			return nil, errors.New("enter the server and the folder")
		}
		user := ""
		if n.User != "" {
			user = n.User + "@"
		}
		r.Location = "sftp://" + user + strings.TrimSpace(n.Host) + joinDir(n.Dir)
		r.Remote.IdentityFile = n.Identity
	case Server:
		if n.URL == "" || n.ServerRepo == "" {
			return nil, errors.New("enter the server and the repository name")
		}
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(n.URL), "https://"), "http://"), "/")
		r.Location = "herakles://" + host + "/" + strings.Trim(n.ServerRepo, "/")
		r.Remote.Username, r.Remote.Password, r.Remote.CACert = n.User, n.Pass, n.CA
	default:
		return nil, fmt.Errorf("unknown storage type %q", n.Type)
	}
	if p.Maintenance {
		r.Maintenance = config.Maintenance{PruneSchedule: "30 4 * * 0", RepackThreshold: 0.2, CheckSchedule: "0 5 1 * *", CheckReadData: "5%"}
	}
	return r, nil
}

func joinDir(d string) string {
	d = strings.Trim(strings.TrimSpace(d), "/")
	if d == "" {
		return ""
	}
	return "/" + d
}

// Job builds the job.
func (p *Plan) Job(repo string) (*config.Job, error) {
	if !validName.MatchString(p.Name) {
		return nil, errors.New("use letters, digits, dot, dash or underscore for the job name")
	}
	j := &config.Job{Name: p.Name, Description: p.Description, Repository: repo, CatchUp: true}
	switch p.Kind {
	case Files:
		for _, x := range p.Paths {
			if x = strings.TrimSpace(x); x != "" {
				j.Paths = append(j.Paths, x)
			}
		}
		if len(j.Paths) == 0 {
			return nil, errors.New("enter at least one folder")
		}
		if p.SkipCaches {
			j.Exclude = append(j.Exclude, "**/.cache", "*.tmp", "*.swp")
			j.ExcludeCaches = true
		}
		if p.SkipBuild {
			j.Exclude = append(j.Exclude, "**/node_modules", "**/target", "**/.venv", "**/__pycache__")
		}
		if p.SkipDownloads {
			j.Exclude = append(j.Exclude, "**/Downloads", "**/.local/share/Trash", "**/$RECYCLE.BIN")
		}
		j.Exclude = append(j.Exclude, p.Extra...)
		j.OneFileSystem = p.OneFS
	case Docker:
		if p.Docker == nil {
			p.Docker = &config.DockerJob{}
		}
		j.Docker = p.Docker
	case Disk:
		if strings.TrimSpace(p.Device) == "" {
			return nil, errors.New("enter the device")
		}
		j.Devices = []string{strings.TrimSpace(p.Device)}
	default:
		return nil, errors.New("choose what to back up")
	}
	h, m := 2, 0
	if p.Time != "" {
		if _, err := fmt.Sscanf(p.Time, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("invalid time %q (HH:MM)", p.Time)
		}
	}
	at := fmt.Sprintf("%d %d", m, h)
	switch p.Schedule {
	case "nightly", "":
		j.Schedules = []config.Schedule{{Cron: at + " * * *", Type: "auto"}}
		j.FullInterval = "7d"
	case "weekly":
		j.Schedules = []config.Schedule{{Cron: at + " * * 0", Type: "full"}, {Cron: at + " * * 1-6", Type: "incremental"}}
	case "hourly":
		j.Schedules = []config.Schedule{{Cron: "0 */4 * * *", Type: "auto"}}
		j.FullInterval = "7d"
	case "manual":
		j.FullInterval = "7d"
	default:
		return nil, fmt.Errorf("unknown schedule %q", p.Schedule)
	}
	switch p.Keep {
	case "standard", "":
		j.Retention = retention.Policy{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12}
	case "week":
		j.Retention = retention.Policy{KeepDaily: 7}
	case "long":
		j.Retention = retention.Policy{KeepDaily: 14, KeepWeekly: 8, KeepMonthly: 24, KeepYearly: 5}
	case "all":
	default:
		return nil, fmt.Errorf("unknown retention %q", p.Keep)
	}
	return j, nil
}

// StorePassword writes a repository password to <dir>/<repo>.password
// (mode 0600) and returns the path.
func StorePassword(dir, repo, password string) (string, error) {
	if !validName.MatchString(repo) {
		return "", errors.New("invalid repository name")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, repo+".password")
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("%s exists already; choose another repository name", p)
	}
	return p, os.WriteFile(p, []byte(password+"\n"), 0o600)
}
