package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// REST talks to a Herakles repository server (see package server).
type REST struct {
	base   *url.URL // .../reponame/
	client *http.Client
	user   string
	pass   string
}

// NewREST creates a client for the repository at rawURL, e.g.
// https://host:8443/myrepo.
func NewREST(rawURL string, opts Options) (*REST, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("rest backend: unsupported scheme %q", u.Scheme)
	}
	r := &REST{user: opts.Username, pass: opts.Password}
	if u.User != nil {
		if r.user == "" {
			r.user = u.User.Username()
		}
		if p, ok := u.User.Password(); ok && r.pass == "" {
			r.pass = p
		}
		u.User = nil
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	r.base = u
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: opts.InsecureSkipVerify} //nolint:gosec // opt-in
	if opts.CACertFile != "" {
		pem, err := os.ReadFile(opts.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", opts.CACertFile)
		}
		tlsCfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	tr.MaxIdleConnsPerHost = 16
	r.client = &http.Client{Transport: tr, Timeout: 10 * time.Minute}
	return r, nil
}

// Location implements Backend.
func (r *REST) Location() string { return r.base.String() }

func (r *REST) url(h Handle) string {
	u := *r.base
	if h.Type == ConfigFile {
		u.Path += "config"
	} else {
		u.Path += string(h.Type) + "/" + h.Name
	}
	return u.String()
}

func (r *REST) do(ctx context.Context, method, u string, body []byte, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if r.user != "" {
		req.SetBasicAuth(r.user, r.pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	return r.client.Do(req)
}

func checkResp(resp *http.Response, h fmt.Stringer, ok ...int) error {
	for _, c := range ok {
		if resp.StatusCode == c {
			return nil
		}
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%v: %w", h, ErrNotFound)
	case http.StatusUnauthorized:
		return fmt.Errorf("server: authentication failed")
	case http.StatusForbidden:
		return fmt.Errorf("server: access denied for %v: %s", h, strings.TrimSpace(string(msg)))
	}
	return fmt.Errorf("server: %s for %v: %s", resp.Status, h, strings.TrimSpace(string(msg)))
}

type strName string

func (s strName) String() string { return string(s) }

// Create implements Backend.
func (r *REST) Create(ctx context.Context) error {
	resp, err := r.do(ctx, http.MethodPost, r.base.String()+"?create=true", nil, nil)
	if err != nil {
		return err
	}
	if err := checkResp(resp, strName("repository"), http.StatusOK, http.StatusCreated); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Save implements Backend.
func (r *REST) Save(ctx context.Context, h Handle, data []byte) error {
	if err := h.Valid(); err != nil {
		return err
	}
	resp, err := r.do(ctx, http.MethodPost, r.url(h), data, nil)
	if err != nil {
		return err
	}
	if err := checkResp(resp, h, http.StatusOK, http.StatusCreated); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Load implements Backend.
func (r *REST) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	if err := h.Valid(); err != nil {
		return nil, err
	}
	hdr := map[string]string{}
	if offset > 0 || length > 0 {
		if length > 0 {
			hdr["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
		} else {
			hdr["Range"] = fmt.Sprintf("bytes=%d-", offset)
		}
	}
	resp, err := r.do(ctx, http.MethodGet, r.url(h), nil, hdr)
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, h, http.StatusOK, http.StatusPartialContent); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK && (offset > 0 || length > 0) {
		// Server ignored the range request; emulate it.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			resp.Body.Close()
			return nil, err
		}
		if length > 0 {
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(resp.Body, length), resp.Body}, nil
		}
	}
	return resp.Body, nil
}

// Stat implements Backend.
func (r *REST) Stat(ctx context.Context, h Handle) (FileInfo, error) {
	if err := h.Valid(); err != nil {
		return FileInfo{}, err
	}
	resp, err := r.do(ctx, http.MethodHead, r.url(h), nil, nil)
	if err != nil {
		return FileInfo{}, err
	}
	if err := checkResp(resp, h, http.StatusOK); err != nil {
		return FileInfo{}, err
	}
	resp.Body.Close()
	return FileInfo{Name: h.Name, Size: resp.ContentLength}, nil
}

// Remove implements Backend.
func (r *REST) Remove(ctx context.Context, h Handle) error {
	if err := h.Valid(); err != nil {
		return err
	}
	resp, err := r.do(ctx, http.MethodDelete, r.url(h), nil, nil)
	if err != nil {
		return err
	}
	if err := checkResp(resp, h, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ListEntry is the JSON format of directory listings.
type ListEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// List implements Backend.
func (r *REST) List(ctx context.Context, t FileType, fn func(FileInfo) error) error {
	u := *r.base
	u.Path += string(t) + "/"
	resp, err := r.do(ctx, http.MethodGet, u.String(), nil, nil)
	if err != nil {
		return err
	}
	if err := checkResp(resp, strName(string(t)), http.StatusOK); err != nil {
		return err
	}
	defer resp.Body.Close()
	var entries []ListEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("decode listing: %w", err)
	}
	for _, e := range entries {
		if err := fn(FileInfo{Name: e.Name, Size: e.Size}); err != nil {
			return err
		}
	}
	return nil
}

// Close implements Backend.
func (r *REST) Close() error {
	r.client.CloseIdleConnections()
	return nil
}
