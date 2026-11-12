// Package dbdump dumps and restores databases that run on the host with
// their own client tools: pg_dump/psql, mysqldump/mysql (or the mariadb
// equivalents), mongodump/mongorestore and sqlite3. Dumps are streamed, so
// they never need space on the disk.
package dbdump

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lamisator/herakles/internal/config"
)

// Dump is one file a databases entry produces.
type Dump struct {
	// Database is the database name ("" for the Postgres roles and a whole
	// MongoDB server; the file path for SQLite).
	Database string `json:"database,omitempty"`
	File     string `json:"file"`           // file name in the entry's snapshot directory
	Size     int64  `json:"size,omitempty"` // expected size (0: unknown)
}

// GlobalsFile holds the Postgres roles and tablespaces.
const GlobalsFile = "_globals.sql"

// Extension of the dump files of a database type.
func Extension(kind string) string {
	switch kind {
	case config.DBMongo:
		return ".archive"
	default:
		return ".sql"
	}
}

// Plan lists what a databases entry dumps, asking the server which
// databases exist.
func Plan(ctx context.Context, d *config.Database) ([]Dump, error) {
	c, err := newClient(d)
	if err != nil {
		return nil, err
	}
	defer c.close()
	var out []Dump
	switch d.Type {
	case config.DBPostgres:
		rows, err := c.query(ctx, "psql", c.pgArgs("-XAtq", "-F", "\t", "-d", c.pgMaintDB(), "-c",
			"SELECT datname, pg_database_size(datname), (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1")...)
		if err != nil {
			return nil, err
		}
		super := false
		for _, r := range rows {
			if len(r) < 3 {
				continue
			}
			super = r[2] == "t"
			if !c.selected(r[0]) {
				continue
			}
			size, _ := strconv.ParseInt(r[1], 10, 64)
			out = append(out, Dump{Database: r[0], File: fileName(r[0], ".sql"), Size: size})
		}
		// The roles need a superuser; databases restored elsewhere need them.
		if super && len(out) > 0 {
			out = append([]Dump{{File: GlobalsFile}}, out...)
		}
	case config.DBMySQL:
		rows, err := c.query(ctx, c.mysqlTool("mysql"), c.mysqlArgs("-N", "-B", "-e",
			"SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name GROUP BY s.schema_name ORDER BY 1")...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			switch strings.ToLower(r[0]) {
			case "information_schema", "performance_schema", "sys":
				continue
			}
			if !c.selected(r[0]) {
				continue
			}
			var size int64
			if len(r) > 1 {
				size, _ = strconv.ParseInt(r[1], 10, 64)
			}
			out = append(out, Dump{Database: r[0], File: fileName(r[0], ".sql"), Size: size})
		}
	case config.DBMongo:
		if len(d.Databases) == 0 {
			out = append(out, Dump{File: "all.archive"})
		}
		for _, db := range d.Databases {
			if c.selected(db) {
				out = append(out, Dump{Database: db, File: fileName(db, ".archive")})
			}
		}
	case config.DBSQLite:
		seen := map[string]bool{}
		for _, pat := range d.Databases {
			files, _ := filepath.Glob(config.ExpandHome(pat))
			if len(files) == 0 && !strings.ContainsAny(pat, "*?[") {
				files = []string{config.ExpandHome(pat)} // reported when dumped
			}
			sort.Strings(files)
			for _, f := range files {
				base := filepath.Base(f)
				if !c.selected(base) && !c.selected(f) {
					continue
				}
				name := fileName(strings.TrimSuffix(base, filepath.Ext(base)), ".sql")
				for i := 2; seen[name]; i++ {
					name = fileName(fmt.Sprintf("%s-%d", strings.TrimSuffix(base, filepath.Ext(base)), i), ".sql")
				}
				seen[name] = true
				var size int64
				if fi, err := os.Stat(f); err == nil {
					size = fi.Size()
				}
				out = append(out, Dump{Database: f, File: name, Size: size})
			}
		}
	default:
		return nil, fmt.Errorf("unknown database type %q", d.Type)
	}
	if len(out) == 0 {
		return nil, errors.New("no databases found")
	}
	return out, nil
}

// fileName makes a database name safe as a file name.
func fileName(name, ext string) string {
	r := strings.NewReplacer("/", "_", `\`, "_", "\x00", "_")
	name = r.Replace(name)
	if name == "" || name == "." || name == ".." || name == strings.TrimSuffix(GlobalsFile, ".sql") {
		name = "_" + name
	}
	return name + ext
}

// Open starts a dump. Reading returns the dump; Close waits for the tool
// and returns its error, with what it printed.
func Open(ctx context.Context, d *config.Database, dump Dump) (io.ReadCloser, error) {
	c, err := newClient(d)
	if err != nil {
		return nil, err
	}
	var tool string
	var args []string
	switch d.Type {
	case config.DBPostgres:
		if dump.File == GlobalsFile && dump.Database == "" {
			tool, args = "pg_dumpall", c.pgArgs("--globals-only", "--clean", "--if-exists")
		} else {
			tool, args = "pg_dump", c.pgArgs("--format=plain", "--clean", "--if-exists", "--dbname", dump.Database)
		}
	case config.DBMySQL:
		tool, args = c.mysqlTool("mysqldump"), c.mysqlArgs("--single-transaction", "--routines", "--events", "--triggers", "--hex-blob", "--default-character-set=utf8mb4")
		args = append(append(args, d.Options...), "--", dump.Database)
	case config.DBMongo:
		tool, args = "mongodump", c.mongoArgs("--archive", "--quiet")
		if dump.Database != "" {
			args = append(args, "--db", dump.Database)
		}
		for _, x := range d.Exclude {
			args = append(args, "--nsExclude", x+".*")
		}
	case config.DBSQLite:
		if _, err := os.Stat(dump.Database); err != nil {
			c.close()
			return nil, err
		}
		tool, args = "sqlite3", append(append([]string{"-batch", "-bail"}, d.Options...), dump.Database, ".dump")
	default:
		c.close()
		return nil, fmt.Errorf("unknown database type %q", d.Type)
	}
	if d.Type == config.DBPostgres || d.Type == config.DBMongo {
		args = append(args, d.Options...)
	}
	cmd, err := c.command(ctx, tool, args...)
	if err != nil {
		c.close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.close()
		return nil, err
	}
	stderr := &tail{n: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		c.close()
		return nil, toolError(tool, err)
	}
	return &dumpReader{r: stdout, cmd: cmd, stderr: stderr, tool: tool, c: c}, nil
}

type dumpReader struct {
	r      io.ReadCloser
	cmd    *exec.Cmd
	stderr *tail
	tool   string
	c      *client
	n      int64
}

func (d *dumpReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.n += int64(n)
	return n, err
}

func (d *dumpReader) Close() error {
	// Drain so the tool is not killed by a closed pipe after a read error.
	_, _ = io.Copy(io.Discard, d.r)
	err := d.cmd.Wait()
	d.c.close()
	if err != nil {
		if msg := d.stderr.String(); msg != "" {
			return toolMessage(d.tool, msg)
		}
		return fmt.Errorf("%s: %w", d.tool, err)
	}
	if d.n == 0 {
		return fmt.Errorf("%s: empty dump", d.tool)
	}
	return nil
}

// RestoreOptions control a restore.
type RestoreOptions struct {
	// Into is the target database (default: the dumped one). For SQLite it
	// is the file to create.
	Into string
	// Replace allows replacing an existing SQLite file.
	Replace bool
}

// Restore loads a dump made by Open into the server.
func Restore(ctx context.Context, d *config.Database, dump Dump, r io.Reader, o RestoreOptions) error {
	c, err := newClient(d)
	if err != nil {
		return err
	}
	defer c.close()
	into := o.Into
	if into == "" {
		into = dump.Database
	}
	switch d.Type {
	case config.DBPostgres:
		if dump.Database == "" && dump.File == GlobalsFile {
			// Existing roles make some statements fail; the rest applies.
			return c.run(ctx, r, nil, "psql", c.pgArgs("-Xq", "-d", c.pgMaintDB(), "-f", "-")...)
		}
		if into == "" {
			return errors.New("name the target database")
		}
		rows, err := c.query(ctx, "psql", c.pgArgs("-XAtq", "-d", c.pgMaintDB(), "-c", "SELECT 1 FROM pg_database WHERE datname = "+pgString(into))...)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if err := c.run(ctx, nil, nil, "psql", c.pgArgs("-Xq", "-d", c.pgMaintDB(), "-c", "CREATE DATABASE "+pgIdent(into))...); err != nil {
				return err
			}
		}
		return c.run(ctx, r, nil, "psql", c.pgArgs("-Xq", "-v", "ON_ERROR_STOP=1", "--single-transaction", "-d", into, "-f", "-")...)
	case config.DBMySQL:
		if into == "" {
			return errors.New("name the target database")
		}
		if err := c.run(ctx, nil, nil, c.mysqlTool("mysql"), c.mysqlArgs("-e", "CREATE DATABASE IF NOT EXISTS "+mysqlIdent(into))...); err != nil {
			return err
		}
		return c.run(ctx, r, nil, c.mysqlTool("mysql"), c.mysqlArgs("--default-character-set=utf8mb4", "--", into)...)
	case config.DBMongo:
		args := c.mongoArgs("--archive", "--drop", "--quiet")
		if dump.Database != "" && into != dump.Database {
			args = append(args, "--nsFrom", dump.Database+".*", "--nsTo", into+".*")
		} else if o.Into != "" && dump.Database == "" {
			args = append(args, "--nsInclude", o.Into+".*")
		}
		return c.run(ctx, r, nil, "mongorestore", args...)
	case config.DBSQLite:
		if into == "" {
			return errors.New("name the target file")
		}
		if _, err := os.Stat(into); err == nil && !o.Replace {
			return fmt.Errorf("%s exists; restore into another file or replace it", into)
		}
		tmp := into + ".herakles-restore"
		os.Remove(tmp)
		if err := c.run(ctx, r, nil, "sqlite3", "-batch", "-bail", tmp); err != nil {
			os.Remove(tmp)
			return err
		}
		if fi, err := os.Stat(into); err == nil {
			_ = os.Chmod(tmp, fi.Mode().Perm())
			chownLike(tmp, fi)
		}
		return os.Rename(tmp, into)
	}
	return fmt.Errorf("unknown database type %q", d.Type)
}

func pgString(s string) string   { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func pgIdent(s string) string    { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func mysqlIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// client knows how to run one entry's tools.
type client struct {
	d      *config.Database
	pass   string
	runAs  *account
	tmpDir string // credential files
	cnf    string // MySQL defaults file / mongodump config file
}

func newClient(d *config.Database) (*client, error) {
	pass, err := d.DBPassword()
	if err != nil {
		return nil, fmt.Errorf("database password: %w", err)
	}
	c := &client{d: d, pass: pass}
	runAs := d.RunAs
	if runAs == "" && d.Type == config.DBPostgres && d.Host == "" && d.User == "" && os.Geteuid() == 0 {
		// A local server trusts its own system user (peer authentication).
		if _, err := lookupAccount("postgres"); err == nil {
			runAs = "postgres"
		}
	}
	if runAs != "" {
		acc, err := lookupAccount(runAs)
		if err != nil {
			return nil, fmt.Errorf("run_as: %w", err)
		}
		c.runAs = acc
	}
	switch {
	case d.Type == config.DBMySQL && pass != "":
		var b strings.Builder
		b.WriteString("[client]\n")
		fmt.Fprintf(&b, "password=\"%s\"\n", strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(pass))
		if err := c.writeSecret("my.cnf", b.String()); err != nil {
			return nil, err
		}
	case d.Type == config.DBMongo && pass != "":
		if err := c.writeSecret("mongo.yaml", "password: "+strconv.Quote(pass)+"\n"); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// writeSecret writes a credentials file readable only by the tool's user.
func (c *client) writeSecret(name, content string) error {
	dir, err := os.MkdirTemp("", "herakles-db-")
	if err != nil {
		return err
	}
	c.tmpDir = dir
	c.cnf = filepath.Join(dir, name)
	if err := os.WriteFile(c.cnf, []byte(content), 0o600); err != nil {
		return err
	}
	if c.runAs != nil {
		for _, p := range []string{dir, c.cnf} {
			if err := os.Chown(p, c.runAs.uid, c.runAs.gid); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *client) close() {
	if c.tmpDir != "" {
		os.RemoveAll(c.tmpDir)
		c.tmpDir = ""
	}
}

func (c *client) selected(name string) bool {
	d := c.d
	for _, x := range d.Exclude {
		if ok, _ := path.Match(x, name); ok {
			return false
		}
	}
	if len(d.Databases) == 0 || d.Type == config.DBSQLite || d.Type == config.DBMongo {
		return true
	}
	for _, x := range d.Databases {
		if ok, _ := path.Match(x, name); ok {
			return true
		}
	}
	return false
}

func (c *client) pgArgs(extra ...string) []string {
	var args []string
	if c.d.Host != "" {
		args = append(args, "-h", c.d.Host)
	}
	if c.d.Port != 0 {
		args = append(args, "-p", strconv.Itoa(c.d.Port))
	}
	if c.d.User != "" {
		args = append(args, "-U", c.d.User)
	}
	return append(args, extra...)
}

// pgMaintDB is the database used for listing and creating databases.
func (c *client) pgMaintDB() string { return "postgres" }

func (c *client) mysqlArgs(extra ...string) []string {
	var args []string
	if c.cnf != "" {
		args = append(args, "--defaults-extra-file="+c.cnf) // must come first
	}
	if c.d.User != "" {
		args = append(args, "--user="+c.d.User)
	}
	switch {
	case strings.HasPrefix(c.d.Host, "/"):
		args = append(args, "--socket="+c.d.Host)
	case c.d.Host != "":
		args = append(args, "--host="+c.d.Host)
	}
	if c.d.Port != 0 {
		args = append(args, "--port="+strconv.Itoa(c.d.Port))
	}
	return append(args, extra...)
}

// mysqlTool prefers the MariaDB names: newer MariaDB releases warn about
// (or no longer ship) the mysql ones.
func (c *client) mysqlTool(name string) string {
	alt := map[string]string{"mysql": "mariadb", "mysqldump": "mariadb-dump"}[name]
	if alt != "" {
		if _, err := c.lookPath(alt); err == nil {
			return alt
		}
	}
	return name
}

func (c *client) mongoArgs(extra ...string) []string {
	var args []string
	if c.d.URI != "" {
		args = append(args, "--uri="+c.d.URI)
	}
	if c.d.Host != "" {
		args = append(args, "--host="+c.d.Host)
	}
	if c.d.Port != 0 {
		args = append(args, "--port="+strconv.Itoa(c.d.Port))
	}
	if c.d.User != "" {
		args = append(args, "--username="+c.d.User, "--authenticationDatabase=admin")
	}
	if c.cnf != "" {
		args = append(args, "--config="+c.cnf)
	}
	return append(args, extra...)
}

// lookPath finds a tool in bin_dir, on PATH or, for Postgres, in the
// versioned directories of Debian and Ubuntu (newest first).
func (c *client) lookPath(tool string) (string, error) {
	if c.d.BinDir != "" {
		p := filepath.Join(config.ExpandHome(c.d.BinDir), tool)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%s not found in %s", tool, c.d.BinDir)
	}
	if p, err := exec.LookPath(tool); err == nil {
		return p, nil
	}
	if c.d.Type == config.DBPostgres {
		dirs, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
		sort.Slice(dirs, func(i, j int) bool { return pgVersion(dirs[i]) > pgVersion(dirs[j]) })
		for _, d := range dirs {
			if p := filepath.Join(d, tool); fileExists(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s not found; install the %s client tools or set bin_dir", tool, c.d.Type)
}

func pgVersion(dir string) float64 {
	v, _ := strconv.ParseFloat(filepath.Base(filepath.Dir(dir)), 64)
	return v
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (c *client) command(ctx context.Context, tool string, args ...string) (*exec.Cmd, error) {
	bin, err := c.lookPath(tool)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	env := os.Environ()
	if c.pass != "" && c.d.Type == config.DBPostgres {
		env = append(env, "PGPASSWORD="+c.pass)
	}
	if c.d.Type == config.DBPostgres {
		env = append(env, "PGAPPNAME=herakles")
	}
	if c.runAs != nil {
		env = append(env, "HOME="+c.runAs.home, "USER="+c.runAs.name, "LOGNAME="+c.runAs.name)
		cmd.Dir = "/" // the tool's user may not enter ours
		setCredential(cmd, c.runAs)
	}
	cmd.Env = env
	return cmd, nil
}

// run runs a tool with stdin and returns its error with what it printed.
func (c *client) run(ctx context.Context, stdin io.Reader, stdout io.Writer, tool string, args ...string) error {
	cmd, err := c.command(ctx, tool, args...)
	if err != nil {
		return err
	}
	cmd.Stdin = stdin
	if stdout == nil {
		stdout = io.Discard
	}
	cmd.Stdout = stdout
	stderr := &tail{n: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if msg := stderr.String(); msg != "" {
			return toolMessage(tool, msg)
		}
		return toolError(tool, err)
	}
	return nil
}

// query runs a tool and splits its output into tab-separated rows.
func (c *client) query(ctx context.Context, tool string, args ...string) ([][]string, error) {
	var out bytes.Buffer
	if err := c.run(ctx, nil, &out, tool, args...); err != nil {
		return nil, err
	}
	var rows [][]string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

// toolMessage is a tool's error output, named after the tool unless it
// names itself.
func toolMessage(tool, msg string) error {
	if strings.HasPrefix(msg, tool+":") {
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %s", tool, msg)
}

func toolError(tool string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed", tool)
	}
	return fmt.Errorf("%s: %w", tool, err)
}

// tail keeps the last n bytes written (a tool's error output).
type tail struct {
	n   int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }
