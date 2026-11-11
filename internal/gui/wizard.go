package gui

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/lamisator/herakles/internal/blockdev"
	"github.com/lamisator/herakles/internal/docker"
)

var safeRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// wizardRoutes serve the backup wizard: facts about this computer and a
// place to store a repository password for unattended backups.
func (s *Server) wizardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/wizard/info", func(w http.ResponseWriter, r *http.Request) {
		home, _ := os.UserHomeDir()
		host, _ := os.Hostname()
		out := map[string]any{"os": runtime.GOOS, "home": home, "hostname": host, "root": os.Geteuid() == 0,
			"config_dir": filepath.Dir(s.eng.Config().Path())}
		dc := &docker.Client{}
		for _, j := range s.eng.Config().Jobs {
			if j.Docker != nil && j.Docker.Binary != "" {
				dc = &docker.Client{Binary: j.Docker.Binary}
				break
			}
		}
		if ps, err := dc.Projects(r.Context(), docker.Search{}); err == nil {
			out["docker"] = true
			out["docker_projects"] = len(ps)
		}
		var suggest []string
		for _, p := range []string{home, "/etc", "/srv", "/var/www", "/opt", `C:\Users`} {
			if p == "" || (runtime.GOOS == "windows") != strings.Contains(p, `\`) {
				continue
			}
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				suggest = append(suggest, p)
			}
		}
		out["suggested_paths"] = suggest
		writeJSON(w, 200, out)
	})
	// Disks and partitions for device backups.
	mux.HandleFunc("GET /api/wizard/devices", func(w http.ResponseWriter, r *http.Request) {
		devs, err := blockdev.List()
		if err != nil {
			writeJSON(w, 200, map[string]any{"devices": []blockdev.Device{}, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"devices": devs, "root": os.Geteuid() == 0})
	})
	// Store a repository password in a file next to the configuration
	// (mode 0600) and return its path for password_file.
	mux.HandleFunc("POST /api/repos/{name}/password-file", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var req struct{ Password string }
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		if !safeRepoName.MatchString(name) {
			s.fail(w, errors.New("repository names are letters, digits and . _ -"), "")
			return
		}
		if len(req.Password) < 8 {
			s.fail(w, errors.New("use a password with at least 8 characters"), "")
			return
		}
		dir := filepath.Dir(s.eng.Config().Path())
		if err := os.MkdirAll(dir, 0o700); err != nil {
			s.fail(w, err, "")
			return
		}
		p := filepath.Join(dir, name+".password")
		if _, err := os.Stat(p); err == nil {
			s.fail(w, errors.New(p+" exists already; choose another repository name"), "")
			return
		}
		if err := os.WriteFile(p, []byte(req.Password+"\n"), 0o600); err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]string{"path": p})
	})
}
