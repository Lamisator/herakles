package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/dbdump"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
)

// fakePostgres writes psql, pg_dump and pg_dumpall scripts that serve the
// databases app, wiki and broken (whose dump fails). psql -f - writes what
// it receives to $FAKE_OUT/loaded.
func fakePostgres(t *testing.T) (bin, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	bin, out = t.TempDir(), t.TempDir()
	t.Setenv("FAKE_OUT", out)
	scripts := map[string]string{
		"psql": `for a; do case "$a" in *"SELECT datname, pg_database_size"*) printf 'app\t2000\tt\nbroken\t10\tt\nwiki\t1000\tt\n'; exit 0;; *"WHERE datname"*) exit 0;; "CREATE DATABASE"*) echo "$a" >> "$FAKE_OUT/created"; exit 0;; esac; done
for a; do [ "$a" = "-f" ] && { cat > "$FAKE_OUT/loaded"; exit 0; }; done
exit 0`,
		"pg_dump": `for a; do db="$a"; done
[ "$db" = broken ] && { echo "pg_dump: error: connection lost" >&2; printf 'partial'; exit 1; }
echo "-- dump of $db"`,
		"pg_dumpall": `echo "-- roles"`,
	}
	for name, body := range scripts {
		write(t, filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n")
		os.Chmod(filepath.Join(bin, name), 0o755)
	}
	return bin, out
}

func TestDatabaseBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	e, _ := setup(t)
	bin, out := fakePostgres(t)
	cfg := e.Config()
	pg := &config.Database{Type: config.DBPostgres, BinDir: bin, Exclude: []string{"wik*"}}
	cfg.Jobs = append(cfg.Jobs, &config.Job{Name: "db", Repository: "main", Databases: []*config.Database{pg}})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunJob(ctx, "db", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "warning" || !strings.Contains(res.Error, "dump of postgres/broken failed: pg_dump: error: connection lost") {
		t.Fatalf("status %s, error %q", res.Status, res.Error)
	}
	if len(res.Databases) != 3 {
		t.Fatalf("dumps: %+v", res.Databases)
	}

	repo, _, err := e.OpenByName(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	snap, err := repo.FindSnapshot(ctx, res.Snapshot, repository.SnapshotFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]repository.DatabaseDump{}
	for _, d := range snap.Databases {
		got[d.Path] = d
	}
	if d := got["/herakles-databases/postgres/app.sql"]; d.Database != "app" || d.Size != int64(len("-- dump of app\n")) || d.Error != "" {
		t.Fatalf("app: %+v", d)
	}
	if d := got["/herakles-databases/postgres/_globals.sql"]; d.Error != "" || d.Size == 0 {
		t.Fatalf("globals: %+v", d)
	}
	if d := got["/herakles-databases/postgres/broken.sql"]; d.Error == "" {
		t.Fatalf("broken: %+v", d)
	}
	content := func(p string) string {
		n, err := repo.FindNode(ctx, snap.Tree, p)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err := restorer.DumpFile(ctx, repo, n, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if c := content("/herakles-databases/postgres/app.sql"); c != "-- dump of app\n" {
		t.Fatalf("stored dump %q", c)
	}
	// A failed dump is left out instead of storing half of it.
	if _, err := repo.FindNode(ctx, snap.Tree, "/herakles-databases/postgres/broken.sql"); err == nil {
		t.Fatal("partial dump stored")
	}

	// Restore into a new database: created, then loaded.
	n, _ := repo.FindNode(ctx, snap.Tree, "/herakles-databases/postgres/app.sql")
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(restorer.DumpFile(ctx, repo, n, pw)) }()
	if err := dbdump.Restore(ctx, pg, dbdump.Dump{Database: "app", File: "app.sql"}, pr, dbdump.RestoreOptions{Into: "app_copy"}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(out, "loaded")) != "-- dump of app\n" || !strings.Contains(read(t, filepath.Join(out, "created")), `CREATE DATABASE "app_copy"`) {
		t.Fatal("restore did not create and load the database")
	}
}

// A job with only databases fails when no server answers.
func TestDatabaseJobUnreachable(t *testing.T) {
	e, _ := setup(t)
	cfg := e.Config()
	cfg.Jobs = append(cfg.Jobs, &config.Job{Name: "db", Repository: "main",
		Databases: []*config.Database{{Type: config.DBMySQL, BinDir: t.TempDir()}}})
	res, err := e.RunJob(context.Background(), "db", RunOptions{})
	if err == nil || res.Status != "failure" || !strings.Contains(err.Error(), "mysql not found") {
		t.Fatalf("%v %+v", err, res)
	}
}

// SQLite needs no server: the files are dumped with sqlite3.
func TestSQLitePlan(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "app.db"), "x")
	write(t, filepath.Join(dir, "b", "app.db"), "y")
	dumps, err := dbdump.Plan(context.Background(), &config.Database{Type: config.DBSQLite, Databases: []string{filepath.Join(dir, "*", "*.db")}})
	if err != nil || len(dumps) != 2 || dumps[0].File != "app.sql" || dumps[1].File != "app-2.sql" {
		t.Fatalf("%+v %v", dumps, err)
	}
}
