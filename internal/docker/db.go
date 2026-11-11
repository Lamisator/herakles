package docker

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// DBKind is a database engine Herakles can dump and restore.
type DBKind string

const (
	Postgres DBKind = "postgres"
	MySQL    DBKind = "mysql" // MySQL and MariaDB
	Mongo    DBKind = "mongo"
)

// imageName returns the repository part of an image reference without
// registry, tag and digest, e.g. "library/postgres" for "postgres:16".
func imageName(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return strings.ToLower(image)
}

// DetectDB recognises database containers by their image.
func DetectDB(ct Container) (DBKind, bool) {
	name := imageName(ct.Image)
	base := name[strings.LastIndex(name, "/")+1:]
	tag := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(ct.Image, strings.SplitN(ct.Image, ":", 2)[0]), ":"))
	if strings.Contains(base, "exporter") || strings.Contains(base, "backup") || strings.Contains(base, "admin") {
		return "", false
	}
	switch {
	case strings.Contains(base, "postgres") || strings.Contains(base, "postgis") || strings.HasPrefix(base, "pgvector") || strings.HasPrefix(base, "timescaledb"),
		strings.HasPrefix(tag, "postgres") || strings.Contains(tag, "-postgresql"):
		return Postgres, true
	case base == "mysql" || base == "mariadb" || base == "percona" || strings.HasPrefix(base, "percona-server") || strings.HasPrefix(base, "mysql-server"):
		return MySQL, true
	case base == "mongo" || base == "mongodb" || base == "mongodb-community-server":
		return Mongo, true
	}
	return "", false
}

// Extension is the dump file extension.
func (k DBKind) Extension() string {
	switch k {
	case Postgres:
		return "sql"
	case MySQL:
		return "mysql.sql"
	case Mongo:
		return "mongo.archive"
	}
	return "dump"
}

// DataDir returns the host path of the container's database files (the
// source of the mount that holds them), or "" if it is not on a mount.
func DataDir(ct Container, kind DBKind) string {
	var dirs []string
	switch kind {
	case Postgres:
		if d := ct.EnvValue("PGDATA"); d != "" {
			dirs = append(dirs, d)
		}
		dirs = append(dirs, "/var/lib/postgresql/data", "/var/lib/postgresql")
	case MySQL:
		dirs = []string{"/var/lib/mysql"}
	case Mongo:
		dirs = []string{"/data/db"}
	}
	for _, d := range dirs {
		for _, m := range ct.Mounts {
			if (m.Type == "bind" || m.Type == "volume") && m.Source != "" && within(d, filepath.Clean(m.Destination)) {
				return filepath.Clean(m.Source)
			}
		}
	}
	return ""
}

// shell runs a script inside the container with sh. Secrets are referenced
// as container variables in the script, so they never appear in the host's
// process list.
func (c *Client) shell(ctx context.Context, ct Container, stdin io.Reader, stdout io.Writer, script string) error {
	args := []string{"exec"}
	if stdin != nil {
		args = append(args, "-i")
	}
	return c.Run(ctx, stdin, stdout, append(args, ct.Name, "sh", "-c", script)...)
}

const mysqlClient = `if command -v mariadb >/dev/null 2>&1; then C=mariadb; D=mariadb-dump; A=mariadb-admin; else C=mysql; D=mysqldump; A=mysqladmin; fi
P="${MARIADB_ROOT_PASSWORD:-$MYSQL_ROOT_PASSWORD}"
F="${MARIADB_ROOT_PASSWORD_FILE:-$MYSQL_ROOT_PASSWORD_FILE}"
[ -z "$P" ] && [ -n "$F" ] && [ -r "$F" ] && P=$(cat "$F")
CNF=$(mktemp); trap 'rm -f "$CNF"' EXIT
printf '[client]\nuser=root\n' > "$CNF"
[ -n "$P" ] && printf 'password="%s"\n' "$(printf %s "$P" | sed 's/[\\"]/\\&/g')" >> "$CNF"
`

const mongoAuth = `U="$MONGO_INITDB_ROOT_USERNAME"; P="$MONGO_INITDB_ROOT_PASSWORD"
[ -z "$P" ] && [ -n "$MONGO_INITDB_ROOT_PASSWORD_FILE" ] && P=$(cat "$MONGO_INITDB_ROOT_PASSWORD_FILE")
[ -z "$U" ] && [ -n "$MONGO_INITDB_ROOT_USERNAME_FILE" ] && U=$(cat "$MONGO_INITDB_ROOT_USERNAME_FILE")
set --
[ -n "$U" ] && set -- --username "$U" --password "$P" --authenticationDatabase admin
`

func pgUser(ct Container) string {
	if u := ct.EnvValue("POSTGRES_USER"); u != "" {
		return u
	}
	return "postgres"
}

// Ready reports whether the database in the container accepts connections.
// Postgres and MySQL are asked over TCP: while a fresh data directory is
// initialised, their images run a temporary server on the socket only.
func (c *Client) Ready(ctx context.Context, ct Container, kind DBKind) error {
	switch kind {
	case Postgres:
		return c.Run(ctx, nil, nil, "exec", ct.Name, "pg_isready", "-q", "-h", "127.0.0.1", "-U", pgUser(ct))
	case MySQL:
		return c.shell(ctx, ct, nil, nil, mysqlClient+`$A --defaults-extra-file="$CNF" --protocol=tcp -h 127.0.0.1 ping --silent`)
	case Mongo:
		return c.shell(ctx, ct, nil, nil, mongoAuth+`if command -v mongosh >/dev/null 2>&1; then S=mongosh; else S=mongo; fi; exec $S --quiet "$@" --eval 'db.adminCommand({ping:1})' >/dev/null`)
	}
	return fmt.Errorf("unknown database kind %q", kind)
}

// WaitReady waits until the database accepts connections.
func (c *Client) WaitReady(ctx context.Context, ct Container, kind DBKind, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := c.Ready(ctx, ct, kind)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: database not ready after %s: %w", ct.Name, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Dump writes a logical dump of all databases of the container to w.
func (c *Client) Dump(ctx context.Context, ct Container, kind DBKind, w io.Writer) error {
	switch kind {
	case Postgres:
		return c.Run(ctx, nil, w, "exec", ct.Name, "pg_dumpall", "-U", pgUser(ct), "--clean", "--if-exists")
	case MySQL:
		return c.shell(ctx, ct, nil, w, mysqlClient+`$D --defaults-extra-file="$CNF" --all-databases --single-transaction --routines --events --triggers`)
	case Mongo:
		return c.shell(ctx, ct, nil, w, mongoAuth+`exec mongodump "$@" --archive --quiet`)
	}
	return fmt.Errorf("unknown database kind %q", kind)
}

// Load imports a dump made by Dump into the container's database.
func (c *Client) Load(ctx context.Context, ct Container, kind DBKind, r io.Reader) error {
	switch kind {
	case Postgres:
		// Statements that fail on a fresh cluster (dropping the current
		// user, creating existing roles) are expected; psql continues.
		return c.Run(ctx, r, nil, "exec", "-i", ct.Name, "psql", "-q", "-X", "-U", pgUser(ct), "-d", "postgres", "-f", "-")
	case MySQL:
		return c.shell(ctx, ct, r, nil, mysqlClient+`$C --defaults-extra-file="$CNF"`)
	case Mongo:
		return c.shell(ctx, ct, r, nil, mongoAuth+`exec mongorestore "$@" --archive --drop --quiet`)
	}
	return fmt.Errorf("unknown database kind %q", kind)
}
