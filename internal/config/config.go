// Package config defines the Herakles YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/chunker"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/retention"
)

// Config is the root of the configuration file.
type Config struct {
	Repositories  []*Repository   `yaml:"repositories" json:"repositories"`
	Jobs          []*Job          `yaml:"jobs" json:"jobs"`
	Daemon        Daemon          `yaml:"daemon,omitempty" json:"daemon"`
	GUI           GUI             `yaml:"gui,omitempty" json:"gui"`
	Tray          Tray            `yaml:"tray,omitempty" json:"tray"`
	Server        Server          `yaml:"server,omitempty" json:"server"`
	Notifications []*Notification `yaml:"notifications,omitempty" json:"notifications"`
	// StateDir stores run history, logs and the daemon token.
	StateDir string `yaml:"state_dir,omitempty" json:"state_dir,omitempty"`

	path string
}

// Repository describes where backups are stored.
type Repository struct {
	Name string `yaml:"name" json:"name"`
	// Location: local path, mounted share or UNC path, sftp://, smb://,
	// rest:https://host/repo or herakles://host/repo (repository server).
	Location        string `yaml:"location" json:"location"`
	Password        string `yaml:"password,omitempty" json:"password,omitempty"`
	PasswordFile    string `yaml:"password_file,omitempty" json:"password_file,omitempty"`
	PasswordEnv     string `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	PasswordCommand string `yaml:"password_command,omitempty" json:"password_command,omitempty"`
	KeyFile         string `yaml:"key_file,omitempty" json:"key_file,omitempty"`
	// Compression: auto (default), none, zstd, zstd:1..4, auto,zstd:N
	Compression      string      `yaml:"compression,omitempty" json:"compression,omitempty"`
	PackSizeMiB      int         `yaml:"pack_size_mib,omitempty" json:"pack_size_mib,omitempty"`
	Connections      int         `yaml:"connections,omitempty" json:"connections,omitempty"`
	UploadLimitKiB   int         `yaml:"upload_limit_kib,omitempty" json:"upload_limit_kib,omitempty"`
	DownloadLimitKiB int         `yaml:"download_limit_kib,omitempty" json:"download_limit_kib,omitempty"`
	Remote           Remote      `yaml:"remote,omitempty" json:"remote"`
	Chunker          ChunkerKiB  `yaml:"chunker,omitempty" json:"chunker"`
	Maintenance      Maintenance `yaml:"maintenance,omitempty" json:"maintenance"`
}

// Remote holds connection settings for remote locations.
type Remote struct {
	Username           string `yaml:"username,omitempty" json:"username,omitempty"`
	Password           string `yaml:"password,omitempty" json:"password,omitempty"`
	PasswordEnv        string `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	Domain             string `yaml:"domain,omitempty" json:"domain,omitempty"`
	IdentityFile       string `yaml:"identity_file,omitempty" json:"identity_file,omitempty"`
	KnownHosts         string `yaml:"known_hosts,omitempty" json:"known_hosts,omitempty"`
	CACert             string `yaml:"ca_cert,omitempty" json:"ca_cert,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty" json:"insecure_skip_verify,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (r Remote) IsZero() bool { return r == Remote{} }

// ChunkerKiB sets chunk sizes in KiB at repository creation.
type ChunkerKiB struct {
	Min int `yaml:"min_kib,omitempty" json:"min_kib,omitempty"`
	Avg int `yaml:"avg_kib,omitempty" json:"avg_kib,omitempty"`
	Max int `yaml:"max_kib,omitempty" json:"max_kib,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (c ChunkerKiB) IsZero() bool { return c == ChunkerKiB{} }

// Maintenance schedules repository-wide tasks in the daemon.
type Maintenance struct {
	PruneSchedule string `yaml:"prune_schedule,omitempty" json:"prune_schedule,omitempty"`
	// RepackThreshold (0-1): repack packs with more unused data than this.
	RepackThreshold float64 `yaml:"repack_threshold,omitempty" json:"repack_threshold,omitempty"`
	CheckSchedule   string  `yaml:"check_schedule,omitempty" json:"check_schedule,omitempty"`
	// CheckReadData: fraction of data to download and verify, e.g. "10%".
	CheckReadData string `yaml:"check_read_data,omitempty" json:"check_read_data,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (m Maintenance) IsZero() bool { return m == Maintenance{} }

// Schedule triggers a backup of a given type.
type Schedule struct {
	// Cron: standard 5-field cron, optional seconds not supported; also
	// descriptors like @daily, @weekly, @every 6h.
	Cron string `yaml:"cron" json:"cron"`
	// Type: auto, full, incremental, differential.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
}

// Hooks run commands around a job.
type Hooks struct {
	Before    []string `yaml:"before,omitempty" json:"before,omitempty"`
	After     []string `yaml:"after,omitempty" json:"after,omitempty"`
	OnSuccess []string `yaml:"on_success,omitempty" json:"on_success,omitempty"`
	OnFailure []string `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`
	Timeout   string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (h Hooks) IsZero() bool {
	return len(h.Before) == 0 && len(h.After) == 0 && len(h.OnSuccess) == 0 && len(h.OnFailure) == 0 && h.Timeout == ""
}

// Retry configures automatic retries of failed scheduled runs.
type Retry struct {
	Attempts int    `yaml:"attempts,omitempty" json:"attempts,omitempty"`
	Delay    string `yaml:"delay,omitempty" json:"delay,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (r Retry) IsZero() bool { return r == Retry{} }

// Job describes what to back up, where and when.
type Job struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Repository  string `yaml:"repository" json:"repository"`
	// Disabled jobs are not scheduled by the daemon.
	Disabled bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Paths    []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// Devices are block devices / partitions backed up as raw images,
	// e.g. /dev/sda1, /dev/nvme0n1 or \\.\PhysicalDrive0, \\.\C:.
	Devices           []string `yaml:"devices,omitempty" json:"devices,omitempty"`
	Exclude           []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	ExcludeIfPresent  []string `yaml:"exclude_if_present,omitempty" json:"exclude_if_present,omitempty"`
	ExcludeCaches     bool     `yaml:"exclude_caches,omitempty" json:"exclude_caches,omitempty"`
	ExcludeLargerThan string   `yaml:"exclude_larger_than,omitempty" json:"exclude_larger_than,omitempty"`
	OneFileSystem     bool     `yaml:"one_file_system,omitempty" json:"one_file_system,omitempty"`
	Tags              []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Hostname          string   `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	// FullInterval promotes "auto" backups to full when the last full backup
	// is older than this (e.g. 7d). Without it auto means incremental.
	FullInterval string     `yaml:"full_interval,omitempty" json:"full_interval,omitempty"`
	Schedules    []Schedule `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// Retention is applied after each successful backup of this job.
	Retention retention.Policy `yaml:"retention,omitempty" json:"retention"`
	// Prune after forgetting snapshots (frees space immediately).
	Prune       bool  `yaml:"prune,omitempty" json:"prune,omitempty"`
	Hooks       Hooks `yaml:"hooks,omitempty" json:"hooks"`
	Retry       Retry `yaml:"retry,omitempty" json:"retry"`
	CatchUp     bool  `yaml:"catch_up,omitempty" json:"catch_up,omitempty"`
	LowPriority bool  `yaml:"low_priority,omitempty" json:"low_priority,omitempty"`
	FileWorkers int   `yaml:"file_workers,omitempty" json:"file_workers,omitempty"`
}

// Daemon configures the background service.
type Daemon struct {
	// ControlListen is the local control API address (default 127.0.0.1:7477).
	ControlListen     string `yaml:"control_listen,omitempty" json:"control_listen,omitempty"`
	MaxConcurrentJobs int    `yaml:"max_concurrent_jobs,omitempty" json:"max_concurrent_jobs,omitempty"`
	LogFile           string `yaml:"log_file,omitempty" json:"log_file,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (d Daemon) IsZero() bool { return d == Daemon{} }

// GUI configures the desktop interface.
type GUI struct {
	Listen      string `yaml:"listen,omitempty" json:"listen,omitempty"`
	OpenBrowser *bool  `yaml:"open_browser,omitempty" json:"open_browser,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (g GUI) IsZero() bool { return g.Listen == "" && g.OpenBrowser == nil }

// Tray configures the desktop tray mode ('herakles tray').
type Tray struct {
	// Autostart starts the tray (daemon + GUI) when the user logs in.
	Autostart bool `yaml:"autostart,omitempty" json:"autostart,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (t Tray) IsZero() bool { return t == Tray{} }

// Server configures the repository server.
type Server struct {
	Listen     string        `yaml:"listen,omitempty" json:"listen,omitempty"`
	DataDir    string        `yaml:"data_dir,omitempty" json:"data_dir,omitempty"`
	TLSCert    string        `yaml:"tls_cert,omitempty" json:"tls_cert,omitempty"`
	TLSKey     string        `yaml:"tls_key,omitempty" json:"tls_key,omitempty"`
	AppendOnly bool          `yaml:"append_only,omitempty" json:"append_only,omitempty"`
	NoAuth     bool          `yaml:"no_auth,omitempty" json:"no_auth,omitempty"`
	Users      []*ServerUser `yaml:"users,omitempty" json:"users,omitempty"`
	// QuotaGiB limits the total size of each repository (0 = unlimited).
	QuotaGiB int `yaml:"quota_gib,omitempty" json:"quota_gib,omitempty"`
}

// IsZero lets yaml omit empty sections.
func (s Server) IsZero() bool {
	return s.Listen == "" && s.DataDir == "" && s.TLSCert == "" && s.TLSKey == "" && !s.AppendOnly && !s.NoAuth && len(s.Users) == 0 && s.QuotaGiB == 0
}

// ServerUser is an account on the repository server.
type ServerUser struct {
	Name         string `yaml:"name" json:"name"`
	PasswordHash string `yaml:"password_hash" json:"password_hash"`
	// Repositories lists glob patterns of repository names this user may
	// access. Default: the user's own name and "<name>-*".
	Repositories []string `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	AppendOnly   bool     `yaml:"append_only,omitempty" json:"append_only,omitempty"`
}

// Notification sends job results somewhere.
type Notification struct {
	// Type: webhook, email or command.
	Type string `yaml:"type" json:"type"`
	// On: success, failure, warning (default: failure, warning).
	On   []string `yaml:"on,omitempty" json:"on,omitempty"`
	Jobs []string `yaml:"jobs,omitempty" json:"jobs,omitempty"`
	// webhook
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// email
	SMTPHost     string   `yaml:"smtp_host,omitempty" json:"smtp_host,omitempty"`
	SMTPPort     int      `yaml:"smtp_port,omitempty" json:"smtp_port,omitempty"`
	SMTPUser     string   `yaml:"smtp_user,omitempty" json:"smtp_user,omitempty"`
	SMTPPassword string   `yaml:"smtp_password,omitempty" json:"smtp_password,omitempty"`
	From         string   `yaml:"from,omitempty" json:"from,omitempty"`
	To           []string `yaml:"to,omitempty" json:"to,omitempty"`
	// command (receives the result as JSON on stdin)
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
}

// Path returns the file the config was loaded from.
func (c *Config) Path() string { return c.path }

// SetPath sets the file used by Save.
func (c *Config) SetPath(p string) { c.path = p }

// DefaultPath returns the default config file location.
func DefaultPath() string {
	if p := os.Getenv("HERAKLES_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		// A system-wide file (used by the Windows service) takes precedence.
		if pd := os.Getenv("ProgramData"); pd != "" {
			p := filepath.Join(pd, "Herakles", "herakles.yaml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "Herakles", "herakles.yaml")
		}
		return "herakles.yaml"
	}
	if os.Geteuid() == 0 {
		return "/etc/herakles/herakles.yaml"
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "herakles", "herakles.yaml")
	}
	return "herakles.yaml"
}

// DefaultStateDir returns the default directory for runtime state.
func DefaultStateDir() string {
	if runtime.GOOS == "windows" {
		if d, err := os.UserCacheDir(); err == nil {
			return filepath.Join(d, "Herakles")
		}
		return "herakles-state"
	}
	if os.Geteuid() == 0 {
		return "/var/lib/herakles"
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "herakles")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "herakles")
	}
	return "herakles-state"
}

// New returns an empty configuration bound to path.
func New(path string) *Config { return &Config{path: path} }

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.path = path
	return c, nil
}

// Parse decodes and validates YAML.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Marshal renders the configuration as YAML.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Herakles configuration — see `herakles config example` for all options.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// Save validates and atomically writes the configuration (mode 0600, since
// it may contain secrets).
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("configuration has no file path")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	data, err := yaml.Marshal(c)
	if err != nil {
		panic(err)
	}
	out := &Config{}
	if err := yaml.Unmarshal(data, out); err != nil {
		panic(err)
	}
	out.path = c.path
	return out
}

// Repo finds a repository by name.
func (c *Config) Repo(name string) *Repository {
	for _, r := range c.Repositories {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Job finds a job by name.
func (c *Config) Job(name string) *Job {
	for _, j := range c.Jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}

// GetStateDir returns the effective state directory.
func (c *Config) GetStateDir() string {
	if c.StateDir != "" {
		return ExpandHome(c.StateDir)
	}
	return DefaultStateDir()
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseCron parses a schedule expression.
func ParseCron(s string) (cron.Schedule, error) { return cronParser.Parse(s) }

// ParseSize parses sizes like 500M, 2G, 10k, 1T or plain bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "IB"), "B")
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			s = s[:n-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(f * float64(mult)), nil
}

// ParsePercent parses "10%" or "0.1" into a fraction.
func ParsePercent(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "%") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil || f < 0 || f > 100 {
			return 0, fmt.Errorf("invalid percentage %q", s)
		}
		return f / 100, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 1 {
		return 0, fmt.Errorf("invalid fraction %q (use 0-1 or a percentage)", s)
	}
	return f, nil
}

// ValidBackupType reports whether t is a known backup type.
func ValidBackupType(t string) bool {
	switch t {
	case "", "auto", "full", "incremental", "differential":
		return true
	}
	return false
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	var errs []error
	add := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	repoNames := map[string]bool{}
	for i, r := range c.Repositories {
		if r == nil {
			add("repositories[%d] is empty", i)
			continue
		}
		if r.Name == "" {
			add("repositories[%d]: name is required", i)
		} else if repoNames[r.Name] {
			add("repository %q defined twice", r.Name)
		}
		repoNames[r.Name] = true
		if r.Location == "" {
			add("repository %q: location is required", r.Name)
		}
		if _, err := compress.Parse(r.Compression); err != nil {
			add("repository %q: %v", r.Name, err)
		}
		if r.PackSizeMiB < 0 || r.PackSizeMiB > 512 {
			add("repository %q: pack_size_mib must be between 1 and 512", r.Name)
		}
		if !r.Chunker.IsZero() {
			if err := r.ChunkerParams().Validate(); err != nil {
				add("repository %q: %v", r.Name, err)
			}
		}
		for _, s := range []string{r.Maintenance.PruneSchedule, r.Maintenance.CheckSchedule} {
			if s != "" {
				if _, err := ParseCron(s); err != nil {
					add("repository %q: invalid schedule %q: %v", r.Name, s, err)
				}
			}
		}
		if _, err := ParsePercent(r.Maintenance.CheckReadData); err != nil {
			add("repository %q: check_read_data: %v", r.Name, err)
		}
	}
	jobNames := map[string]bool{}
	for i, j := range c.Jobs {
		if j == nil {
			add("jobs[%d] is empty", i)
			continue
		}
		if j.Name == "" {
			add("jobs[%d]: name is required", i)
		} else if jobNames[j.Name] {
			add("job %q defined twice", j.Name)
		}
		jobNames[j.Name] = true
		if !repoNames[j.Repository] {
			add("job %q: unknown repository %q", j.Name, j.Repository)
		}
		if len(j.Paths) == 0 && len(j.Devices) == 0 {
			add("job %q: needs at least one path or device", j.Name)
		}
		if _, err := ParseSize(j.ExcludeLargerThan); err != nil {
			add("job %q: exclude_larger_than: %v", j.Name, err)
		}
		if j.FullInterval != "" {
			if _, err := retention.ParseDuration(j.FullInterval); err != nil {
				add("job %q: full_interval: %v", j.Name, err)
			}
		}
		for _, s := range j.Schedules {
			if _, err := ParseCron(s.Cron); err != nil {
				add("job %q: invalid schedule %q: %v", j.Name, s.Cron, err)
			}
			if !ValidBackupType(s.Type) {
				add("job %q: invalid backup type %q (auto, full, incremental, differential)", j.Name, s.Type)
			}
		}
		if err := j.Retention.Validate(); err != nil {
			add("job %q: %v", j.Name, err)
		}
		if j.Hooks.Timeout != "" {
			if _, err := time.ParseDuration(j.Hooks.Timeout); err != nil {
				add("job %q: hooks.timeout: %v", j.Name, err)
			}
		}
		if j.Retry.Delay != "" {
			if _, err := time.ParseDuration(j.Retry.Delay); err != nil {
				add("job %q: retry.delay: %v", j.Name, err)
			}
		}
	}
	for i, n := range c.Notifications {
		switch n.Type {
		case "webhook":
			if n.URL == "" {
				add("notifications[%d]: webhook needs url", i)
			}
		case "email":
			if n.SMTPHost == "" || n.From == "" || len(n.To) == 0 {
				add("notifications[%d]: email needs smtp_host, from and to", i)
			}
		case "command":
			if n.Command == "" {
				add("notifications[%d]: command needs command", i)
			}
		default:
			add("notifications[%d]: unknown type %q (webhook, email, command)", i, n.Type)
		}
		for _, on := range n.On {
			if on != "success" && on != "failure" && on != "warning" {
				add("notifications[%d]: invalid event %q", i, on)
			}
		}
	}
	userNames := map[string]bool{}
	for _, u := range c.Server.Users {
		if u.Name == "" || u.PasswordHash == "" {
			add("server user needs name and password_hash")
		}
		if userNames[u.Name] {
			add("server user %q defined twice", u.Name)
		}
		userNames[u.Name] = true
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		add("server: tls_cert and tls_key must be set together")
	}
	return errors.Join(errs...)
}

// ChunkerParams converts KiB settings (falling back to defaults).
func (r *Repository) ChunkerParams() chunker.Params {
	p := chunker.DefaultParams
	if r.Chunker.Min > 0 {
		p.Min = r.Chunker.Min << 10
	}
	if r.Chunker.Avg > 0 {
		p.Avg = r.Chunker.Avg << 10
	}
	if r.Chunker.Max > 0 {
		p.Max = r.Chunker.Max << 10
	}
	return p
}

// BackendOptions converts remote settings.
func (r *Repository) BackendOptions() backend.Options {
	pw := r.Remote.Password
	if r.Remote.PasswordEnv != "" {
		pw = os.Getenv(r.Remote.PasswordEnv)
	}
	return backend.Options{
		Username: r.Remote.Username, Password: pw, Domain: r.Remote.Domain,
		IdentityFile: ExpandHome(r.Remote.IdentityFile), KnownHostsFile: ExpandHome(r.Remote.KnownHosts),
		CACertFile: ExpandHome(r.Remote.CACert), InsecureSkipVerify: r.Remote.InsecureSkipVerify,
		UploadLimit: r.UploadLimitKiB, DownloadLimit: r.DownloadLimitKiB,
	}
}

// ErrNoPassword is returned when no credential source is configured.
var ErrNoPassword = errors.New("no password or key file configured")

// Credentials resolves the configured password sources and key file.
// Sources are tried in order: password, password_file, password_env,
// password_command, then the HERAKLES_PASSWORD environment variable.
func (r *Repository) Credentials() (crypto.Credentials, error) {
	var c crypto.Credentials
	switch {
	case r.Password != "":
		c.Password = r.Password
	case r.PasswordFile != "":
		data, err := os.ReadFile(ExpandHome(r.PasswordFile))
		if err != nil {
			return c, fmt.Errorf("read password file: %w", err)
		}
		c.Password = strings.TrimRight(string(data), "\r\n")
	case r.PasswordEnv != "":
		c.Password = os.Getenv(r.PasswordEnv)
		if c.Password == "" {
			return c, fmt.Errorf("environment variable %s is empty", r.PasswordEnv)
		}
	case r.PasswordCommand != "":
		out, err := shellCommand(r.PasswordCommand).Output()
		if err != nil {
			return c, fmt.Errorf("password command failed: %w", err)
		}
		c.Password = strings.TrimRight(string(out), "\r\n")
	default:
		c.Password = os.Getenv("HERAKLES_PASSWORD")
	}
	if r.KeyFile != "" {
		data, err := crypto.LoadKeyFile(ExpandHome(r.KeyFile))
		if err != nil {
			return c, err
		}
		c.KeyFileData = data
	}
	if c.Empty() {
		return c, ErrNoPassword
	}
	return c, nil
}

// HasCredentialSource reports whether any non-interactive source exists.
func (r *Repository) HasCredentialSource() bool {
	return r.Password != "" || r.PasswordFile != "" || r.PasswordEnv != "" || r.PasswordCommand != "" || r.KeyFile != "" || os.Getenv("HERAKLES_PASSWORD") != ""
}

func shellCommand(cmd string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", cmd)
	}
	return exec.Command("/bin/sh", "-c", cmd)
}

// ShellCommand builds a platform shell command (used by hooks).
func ShellCommand(cmd string) *exec.Cmd { return shellCommand(cmd) }
