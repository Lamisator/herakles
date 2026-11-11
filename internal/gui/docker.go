package gui

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
)

type projSnapView struct {
	ID      string              `json:"id"`
	Time    time.Time           `json:"time"`
	Type    string              `json:"type"`
	Summary *repository.Summary `json:"summary,omitempty"`
	Stopped bool                `json:"stopped,omitempty"`
	Dumps   []string            `json:"dumps,omitempty"`
	Failed  []string            `json:"failed_dumps,omitempty"`
}

func projSnapViewOf(s *repository.Snapshot) projSnapView {
	v := projSnapView{ID: s.ID().String(), Time: s.Time, Type: s.Type, Summary: s.Summary}
	if s.Docker != nil {
		v.Stopped = s.Docker.Stopped
		for _, d := range s.Docker.Dumps {
			switch {
			case d.File != "":
				v.Dumps = append(v.Dumps, d.Container+" ("+d.Kind+")")
			case d.Error != "" && d.Error != "container not running" && d.Error != "no database server answering in this container":
				v.Failed = append(v.Failed, d.Container+": "+d.Error)
			}
		}
	}
	return v
}

type projectView struct {
	Name       string                `json:"name"`
	Standalone bool                  `json:"standalone,omitempty"`
	WorkingDir string                `json:"working_dir,omitempty"`
	Running    int                   `json:"running"`
	Containers []docker.Container    `json:"containers"`
	Databases  []string              `json:"databases,omitempty"`
	Paths      []string              `json:"paths"`
	Mode       string                `json:"mode"` // dump, stop, files
	Last       *projSnapView         `json:"last,omitempty"`
	Snapshots  int                   `json:"snapshots"`
	Result     *engine.ProjectResult `json:"result,omitempty"`
}

type dockerJobView struct {
	Job        string        `json:"job"`
	Repository string        `json:"repository"`
	Projects   []projectView `json:"projects"`
	Error      string        `json:"error,omitempty"`
}

// hostProject describes a project on the host for the job editor.
type hostProject struct {
	Name       string   `json:"name"`
	WorkingDir string   `json:"working_dir,omitempty"`
	Standalone bool     `json:"standalone,omitempty"`
	Dirs       []string `json:"dirs"`
	Running    int      `json:"running"`
	Containers []string `json:"containers"`
	Databases  []string `json:"databases,omitempty"`
}

func (s *Server) dockerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/docker", s.dockerList)
	// All projects on the host (for choosing them in a job). Query: job (its
	// docker binary), folders and scan (comma separated directories to
	// search for projects without containers).
	mux.HandleFunc("GET /api/docker/host", func(w http.ResponseWriter, r *http.Request) {
		// The job's docker binary, else the one configured for other jobs.
		dc := &docker.Client{}
		if j := s.eng.Config().Job(r.URL.Query().Get("job")); j != nil && j.Docker != nil && j.Docker.Binary != "" {
			dc = engine.DockerClient(j)
		} else {
			for _, j := range s.eng.Config().Jobs {
				if j.Docker != nil && j.Docker.Binary != "" {
					dc = engine.DockerClient(j)
					break
				}
			}
		}
		split := func(v string) []string {
			var out []string
			for _, x := range strings.Split(v, ",") {
				if x = strings.TrimSpace(x); x != "" {
					out = append(out, x)
				}
			}
			return out
		}
		ps, err := dc.Projects(r.Context(), docker.Search{Dirs: split(r.URL.Query().Get("scan")), Trees: split(r.URL.Query().Get("folders"))})
		if err != nil {
			s.fail(w, err, "")
			return
		}
		out := []hostProject{}
		for _, p := range ps {
			hp := hostProject{Name: p.Name, WorkingDir: p.WorkingDir, Standalone: p.Standalone, Dirs: p.Dirs(), Running: p.Running(), Containers: []string{}}
			for _, c := range p.Containers {
				hp.Containers = append(hp.Containers, c.Name)
				if k, ok := docker.DetectDB(c); ok {
					hp.Databases = append(hp.Databases, string(k))
				}
			}
			out = append(out, hp)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/docker/{job}/{project}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		unit := r.PathValue("job") + "/" + r.PathValue("project")
		snaps, err := s.eng.ProjectSnapshots(r.Context(), unit)
		if err != nil {
			s.fail(w, err, s.repoOfJob(r.PathValue("job")))
			return
		}
		out := []projSnapView{}
		for _, sn := range snaps {
			out = append(out, projSnapViewOf(sn))
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/docker/{job}/{project}/backup", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Type string }
		_ = decode(r, &req)
		if req.Type == "" {
			req.Type = "auto"
		}
		unit := r.PathValue("job") + "/" + r.PathValue("project")
		if !s.credentialsReady(w, r.PathValue("job")) {
			return
		}
		run, err := s.runner.Enqueue(daemon.KindBackup, unit, req.Type, "manual")
		if err != nil {
			s.fail(w, err, "")
			return
		}
		v, _ := s.runner.Get(run.ID)
		writeJSON(w, 202, v)
	})
	mux.HandleFunc("POST /api/docker/{job}/{project}/restore", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Snapshot  string `json:"snapshot"`
			Databases string `json:"databases"`
			KeepOld   bool   `json:"keep_old"`
			Confirm   string `json:"confirm"`
		}
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		if req.Confirm != r.PathValue("project") {
			s.fail(w, fmt.Errorf("type the project name %q to confirm", r.PathValue("project")), "")
			return
		}
		if !s.credentialsReady(w, r.PathValue("job")) {
			return
		}
		unit := r.PathValue("job") + "/" + r.PathValue("project")
		params := map[string]string{"snapshot": req.Snapshot, "databases": req.Databases, "keep_old": fmt.Sprint(req.KeepOld)}
		run, err := s.runner.EnqueueParams(daemon.KindRestore, unit, "manual", params)
		if err != nil {
			s.fail(w, err, "")
			return
		}
		who := "local user"
		if u := userOf(r); u != nil {
			who = u.Name
		}
		s.eng.Log.Warn("project restore started", "project", unit, "snapshot", req.Snapshot, "by", who)
		v, _ := s.runner.Get(run.ID)
		writeJSON(w, 202, v)
	})
}

func (s *Server) repoOfJob(job string) string {
	if j := s.eng.Config().Job(job); j != nil {
		return j.Repository
	}
	return ""
}

// credentialsReady makes sure the job's repository can be opened without a
// prompt before a task is queued; otherwise it asks the GUI for the password.
func (s *Server) credentialsReady(w http.ResponseWriter, job string) bool {
	rc := s.eng.Config().Repo(s.repoOfJob(job))
	if rc == nil {
		s.fail(w, fmt.Errorf("unknown job %q", job), "")
		return false
	}
	if _, err := s.eng.Credentials(rc); err != nil {
		s.fail(w, err, rc.Name)
		return false
	}
	return true
}

func (s *Server) dockerList(w http.ResponseWriter, r *http.Request) {
	cfg := s.eng.Config()
	out := []dockerJobView{}
	for _, job := range cfg.Jobs {
		if job.Docker == nil {
			continue
		}
		jv := dockerJobView{Job: job.Name, Repository: job.Repository, Projects: []projectView{}}
		all, ps, err := s.eng.DockerProjectsAll(r.Context(), job)
		if err != nil {
			jv.Error = err.Error()
			out = append(out, jv)
			continue
		}
		sets := docker.DataSets(all, engine.DockerDataOptions(job))
		snaps, err := s.jobSnapshots(r, job)
		if err != nil {
			s.fail(w, err, job.Repository)
			return
		}
		var last *engine.JobResult
		if h := s.eng.History().Last(job.Name); h != nil {
			last = h
		}
		for _, p := range ps {
			pv := projectView{Name: p.Name, Standalone: p.Standalone, WorkingDir: p.WorkingDir, Running: p.Running(), Containers: p.Containers}
			for _, c := range p.Containers {
				if k, ok := docker.DetectDB(c); ok {
					pv.Databases = append(pv.Databases, c.Name+" ("+string(k)+")")
				}
			}
			pv.Paths = sets[p.Name].Paths
			switch {
			case job.Docker.Stops(p.Name):
				pv.Mode = "stop"
			case len(pv.Databases) > 0 && !job.Docker.SkipDumps:
				pv.Mode = "dump"
			default:
				pv.Mode = "files"
			}
			mine := repository.FilterSnapshots(snaps, repository.SnapshotFilter{Job: job.Name + "/" + p.Name})
			pv.Snapshots = len(mine)
			for _, sn := range mine {
				if pv.Last == nil || sn.Time.After(pv.Last.Time) {
					v := projSnapViewOf(sn)
					pv.Last = &v
				}
			}
			if last != nil {
				if i := slices.IndexFunc(last.Projects, func(x engine.ProjectResult) bool { return x.Project == p.Name }); i >= 0 {
					pr := last.Projects[i]
					pv.Result = &pr
				}
			}
			jv.Projects = append(jv.Projects, pv)
		}
		out = append(out, jv)
	}
	writeJSON(w, 200, out)
}

func (s *Server) jobSnapshots(r *http.Request, job *config.Job) ([]*repository.Snapshot, error) {
	repo, _, err := s.eng.OpenByName(r.Context(), job.Repository)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	return repo.Snapshots(r.Context())
}
