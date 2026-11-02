package app

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	cfg     Config
	log     *slog.Logger
	mgr     *Manager
	handler http.Handler
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	tools := Tools{
		YTDLP:            cfg.YTDLP,
		FFmpeg:           cfg.FFmpeg,
		FFprobe:          cfg.FFprobe,
		Cookies:          cfg.Cookies,
		MaxDownloadBytes: cfg.MaxDownloadBytes,
	}
	mgr, err := NewManager(cfg, tools, logger)
	if err != nil {
		return nil, err
	}

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("couldn't load the web assets: %w", err)
	}
	indexHTML, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil, fmt.Errorf("couldn't load the index page: %w", err)
	}
	passwordRequired := strconv.FormatBool(cfg.DownloadPassword != "")
	indexHTML = bytes.ReplaceAll(indexHTML, []byte("__DOWNLOAD_PASSWORD_REQUIRED__"), []byte(passwordRequired))
	passwordFieldHidden := []byte("hidden")
	if cfg.DownloadPassword != "" {
		passwordFieldHidden = nil
	}
	indexHTML = bytes.ReplaceAll(indexHTML, []byte("__DOWNLOAD_PASSWORD_HIDDEN__"), passwordFieldHidden)
	fileServer := http.FileServerFS(static)

	s := &Server{cfg: cfg, log: logger, mgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.deleteJob)
	mux.HandleFunc("GET /media/{id}", s.serveMedia)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexHTML)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))

	s.handler = s.recoverPanics(s.logRequests(s.securityHeaders(s.passwordProtection(mux))))
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) Close() {
	s.mgr.Close()
}

type createJobRequest struct {
	URL      string `json:"url"`
	MaxBytes int64  `json:"maxBytes"`
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.Version})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req createJobRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "that request wasn't valid JSON")
		return
	}

	url, err := ValidateURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := s.mgr.Create(url, req.MaxBytes)
	switch {
	case errors.Is(err, ErrBadTarget):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, ErrBusy):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		s.log.Error("couldn't create job", "error", err)
		writeError(w, http.StatusInternalServerError, "couldn't start the download")
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	view, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	if !s.mgr.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	path, name, modTime, ok := s.mgr.Media(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	defer file.Close()

	if name == "" {
		name = "video.mp4"
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, modTime, file)
}

func (s *Server) passwordProtection(next http.Handler) http.Handler {
	if s.cfg.DownloadPassword == "" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !passwordMatches(r.Header.Get("X-Download-Password"), s.cfg.DownloadPassword) {
			writeError(w, http.StatusUnauthorized, "a valid download password is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

func passwordMatches(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("request panicked", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "something went wrong")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		level := slog.LevelDebug
		if r.Method != http.MethodGet || recorder.statusCode() >= http.StatusBadRequest {
			level = slog.LevelInfo
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode(),
			"bytes", recorder.bytes,
			"duration", time.Since(started).Round(time.Millisecond),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
