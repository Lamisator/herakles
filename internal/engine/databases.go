package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/dbdump"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
)

// DatabasePath is where a dump is stored in the snapshot.
func DatabasePath(d *config.Database, file string) string {
	return path.Join("/", repository.DatabasesDir, d.Label(), file)
}

// databaseStreams asks the job's database servers what to dump and returns
// one stream per dump. The records are completed while the streams are
// read; entries that cannot be listed are recorded with their error.
func (e *Engine) databaseStreams(ctx context.Context, job *config.Job, log *slog.Logger) ([]archiver.Stream, []repository.DatabaseDump) {
	type planned struct {
		d    *config.Database
		dump dbdump.Dump
		rec  int // index in recs
	}
	var plan []planned
	var recs []repository.DatabaseDump
	for _, d := range job.Databases {
		dumps, err := dbdump.Plan(ctx, d)
		if err != nil {
			log.Warn("cannot list databases", "databases", d.Label(), "err", err)
			recs = append(recs, repository.DatabaseDump{Source: d.Label(), Kind: d.Type, Error: err.Error()})
			continue
		}
		for _, dp := range dumps {
			plan = append(plan, planned{d, dp, len(recs)})
			recs = append(recs, repository.DatabaseDump{Source: d.Label(), Kind: d.Type, Database: dp.Database, Path: DatabasePath(d, dp.File)})
		}
	}
	// recs is complete: the streams below update its elements in place,
	// and the archiver stores the same slice in the snapshot.
	var streams []archiver.Stream
	for _, p := range plan {
		rec := &recs[p.rec]
		streams = append(streams, archiver.Stream{
			Path: rec.Path,
			Size: p.dump.Size,
			Open: func(ctx context.Context) (io.ReadCloser, error) {
				log.Info("dumping database", "databases", p.d.Label(), "database", dumpName(p.dump))
				return dbdump.Open(ctx, p.d, p.dump)
			},
			Done: func(size int64, err error) {
				rec.Size = size
				if err != nil {
					rec.Error = err.Error()
					log.Warn("database dump failed", "databases", p.d.Label(), "database", dumpName(p.dump), "err", err)
				}
			},
		})
	}
	return streams, recs
}

func dumpName(d dbdump.Dump) string {
	if d.Database == "" {
		return d.File
	}
	return d.Database
}

// databaseErrors summarises failed dumps for the job result.
func databaseErrors(recs []repository.DatabaseDump) string {
	var msgs []string
	for _, r := range recs {
		if r.Error == "" {
			continue
		}
		what := r.Source
		if r.Database != "" {
			what += "/" + r.Database
		}
		msgs = append(msgs, fmt.Sprintf("dump of %s failed: %s", what, firstLine(r.Error)))
	}
	return strings.Join(msgs, "; ")
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// DatabaseConfig returns the job's databases entry a dump came from, or the
// defaults of its type when the configuration no longer has it.
func (e *Engine) DatabaseConfig(job string, r repository.DatabaseDump) *config.Database {
	if j := e.Config().Job(job); j != nil {
		for _, d := range j.Databases {
			if d.Label() == r.Source && d.Type == r.Kind {
				return d
			}
		}
	}
	return &config.Database{Name: r.Source, Type: r.Kind}
}

// RestoreDatabase loads the dump at dumpPath of a snapshot into the
// database server it came from.
func (e *Engine) RestoreDatabase(ctx context.Context, repo *repository.Repository, snap *repository.Snapshot, dumpPath string, o dbdump.RestoreOptions) error {
	var rec *repository.DatabaseDump
	for i := range snap.Databases {
		if snap.Databases[i].Path == dumpPath && dumpPath != "" {
			rec = &snap.Databases[i]
		}
	}
	if rec == nil {
		return fmt.Errorf("snapshot %s has no database dump %s", snap.ID().Short(), dumpPath)
	}
	if rec.Error != "" {
		return fmt.Errorf("%s was not dumped in this snapshot: %s", dumpPath, firstLine(rec.Error))
	}
	node, err := repo.FindNode(ctx, snap.Tree, rec.Path)
	if err != nil {
		return err
	}
	d := e.DatabaseConfig(snap.Job, *rec)
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(restorer.DumpFile(ctx, repo, node, pw)) }()
	err = dbdump.Restore(ctx, d, dbdump.Dump{Database: rec.Database, File: path.Base(rec.Path)}, pr, o)
	pr.CloseWithError(errors.New("restore stopped"))
	if err == nil {
		into := o.Into
		if into == "" {
			into = rec.Database
		}
		e.Log.Info("database restored", "snapshot", snap.ID().Short(), "dump", rec.Path, "into", into)
	}
	return err
}
