package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	minTargetBytes = 1 << 20
	maxTargetBytes = 8 << 30
)

var (
	ErrBusy      = errors.New("too many jobs are running — try again in a few minutes")
	ErrBadTarget = errors.New("target size must be between 1 MB and 8192 MB")
)

type State string

const (
	StateQueued      State = "queued"
	StateProbing     State = "probing"
	StateDownloading State = "downloading"
	StateProcessing  State = "processing"
	StateDone        State = "done"
	StateError       State = "error"
	StateCanceled    State = "canceled"
)

type JobView struct {
	ID          string    `json:"id"`
	State       State     `json:"state"`
	Phase       string    `json:"phase"`
	Progress    int       `json:"progress"`
	Title       string    `json:"title,omitempty"`
	Duration    float64   `json:"duration,omitempty"`
	SourceBytes int64     `json:"sourceBytes,omitempty"`
	SizeBytes   int64     `json:"sizeBytes,omitempty"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	MaxBytes    int64     `json:"maxBytes,omitempty"`
	MediaURL    string    `json:"mediaUrl,omitempty"`
	FileName    string    `json:"fileName,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Job struct {
	id        string
	url       string
	dir       string
	maxBytes  int64
	createdAt time.Time
	cancel    context.CancelFunc

	mu          sync.Mutex
	state       State
	phase       string
	progress    int
	title       string
	duration    float64
	sourceBytes int64
	sizeBytes   int64
	width       int
	height      int
	filePath    string
	fileName    string
	mediaURL    string
	errMsg      string
	finishedAt  time.Time
}

func (j *Job) snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobView{
		ID:          j.id,
		State:       j.state,
		Phase:       j.phase,
		Progress:    j.progress,
		Title:       j.title,
		Duration:    j.duration,
		SourceBytes: j.sourceBytes,
		SizeBytes:   j.sizeBytes,
		Width:       j.width,
		Height:      j.height,
		MaxBytes:    j.maxBytes,
		MediaURL:    j.mediaURL,
		FileName:    j.fileName,
		Error:       j.errMsg,
		CreatedAt:   j.createdAt,
	}
}

func (j *Job) set(state State, phase string, progress int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == StateDone || j.state == StateError || j.state == StateCanceled {
		return
	}
	j.state = state
	if phase != "" {
		j.phase = phase
	}
	if progress > j.progress {
		j.progress = clampPercent(progress)
	}
}

func (j *Job) setPhase(phase string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == StateDone || j.state == StateError || j.state == StateCanceled {
		return
	}
	j.phase = phase
}

func (j *Job) setProgress(progress int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == StateDone || j.state == StateError || j.state == StateCanceled {
		return
	}
	if progress > j.progress {
		j.progress = clampPercent(progress)
	}
}

func (j *Job) setMeta(title string, duration float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.title = title
	if duration > 0 {
		j.duration = duration
	}
}

func (j *Job) setSourceBytes(size int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sourceBytes = size
}

func (j *Job) complete(path string, info MediaInfo, sourceBytes int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = StateDone
	j.phase = "Ready"
	j.progress = 100
	j.filePath = path
	j.fileName = safeFilename(j.title)
	j.sizeBytes = info.Size
	j.sourceBytes = sourceBytes
	j.duration = info.Duration
	j.width = info.Width
	j.height = info.Height
	j.mediaURL = "/media/" + j.id
	j.finishedAt = time.Now()
}

func (j *Job) fail(err error) {
	state := StateError
	message := err.Error()
	phase := "Failed"
	if errors.Is(err, context.Canceled) {
		state = StateCanceled
		message = "Download canceled"
		phase = "Canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		message = "Processing took too long — try a shorter video"
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == StateDone || j.state == StateError || j.state == StateCanceled {
		return
	}
	j.state = state
	j.phase = phase
	j.errMsg = message
	j.finishedAt = time.Now()
}

func (j *Job) isFinished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == StateDone || j.state == StateError || j.state == StateCanceled
}

type Manager struct {
	tools   Tools
	dataDir string
	cfg     Config
	log     *slog.Logger

	sem  chan struct{}
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu        sync.RWMutex
	jobs      map[string]*Job
	closeOnce sync.Once
}

func NewManager(cfg Config, tools Tools, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("couldn't resolve DATA_DIR: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("couldn't create DATA_DIR: %w", err)
	}
	removeOrphanJobDirs(abs, logger)

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		tools:   tools,
		dataDir: abs,
		cfg:     cfg,
		log:     logger,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		ctx:     ctx,
		stop:    cancel,
		jobs:    make(map[string]*Job),
	}
	go m.cleanupLoop()
	return m, nil
}

func (m *Manager) Create(url string, maxBytes int64) (JobView, error) {
	if maxBytes != 0 && (maxBytes < minTargetBytes || maxBytes > maxTargetBytes) {
		return JobView{}, ErrBadTarget
	}
	if m.ctx.Err() != nil {
		return JobView{}, errors.New("server is shutting down")
	}

	m.mu.Lock()
	if len(m.jobs) >= m.cfg.MaxJobs {
		m.mu.Unlock()
		return JobView{}, ErrBusy
	}
	id, err := newJobID()
	if err != nil {
		m.mu.Unlock()
		return JobView{}, err
	}
	dir := filepath.Join(m.dataDir, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		m.mu.Unlock()
		return JobView{}, fmt.Errorf("couldn't create a workspace: %w", err)
	}

	jobCtx, cancel := context.WithTimeout(m.ctx, m.cfg.JobTimeout)
	job := &Job{
		id:        id,
		url:       url,
		dir:       dir,
		maxBytes:  maxBytes,
		createdAt: time.Now(),
		cancel:    cancel,
		state:     StateQueued,
		phase:     "Queued",
		progress:  1,
	}
	m.jobs[id] = job
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(jobCtx, job)
	return job.snapshot(), nil
}

func (m *Manager) run(ctx context.Context, job *Job) {
	defer m.wg.Done()
	defer job.cancel()
	started := time.Now()

	defer func() {
		if recovered := recover(); recovered != nil {
			m.log.Error("job panicked", "id", job.id, "panic", recovered)
			job.fail(errors.New("internal error while processing the video"))
		}
		if !job.isFinished() {
			job.fail(errors.New("the job stopped unexpectedly"))
		}
		if view := job.snapshot(); view.State != StateDone {
			_ = os.RemoveAll(job.dir)
		}
		view := job.snapshot()
		fields := []any{
			"id", job.id,
			"state", view.State,
			"elapsed", time.Since(started).Round(time.Millisecond),
			"size", view.SizeBytes,
		}
		if view.Error != "" {
			fields = append(fields, "error", view.Error)
		}
		m.log.Info("job finished", fields...)
	}()

	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		job.fail(ctx.Err())
		return
	}

	job.set(StateProbing, "Inspecting link", 2)
	meta, err := m.tools.Probe(ctx, job.url)
	if err != nil {
		job.fail(err)
		return
	}
	if meta.IsLive || meta.LiveStatus == "is_live" || meta.LiveStatus == "is_upcoming" {
		job.fail(errors.New("live streams aren't supported"))
		return
	}
	if meta.Duration > m.cfg.MaxDuration.Seconds() {
		job.fail(fmt.Errorf("this video is longer than the %s limit", durationShort(m.cfg.MaxDuration)))
		return
	}
	// Fail before a large download when the requested target can't possibly fit
	// this duration. If the source is already under the target, it will be
	// served untouched, so only check when the source is likely bigger.
	if job.maxBytes > 0 && meta.Duration > 0 {
		estimated := meta.FilesizeApprox
		if estimated <= 0 {
			estimated = meta.Filesize
		}
		if estimated <= 0 || estimated > job.maxBytes {
			if _, _, err := planBitrate(job.maxBytes, meta.Duration); err != nil {
				job.fail(err)
				return
			}
		}
	}
	job.setMeta(meta.Title, meta.Duration)

	job.set(StateDownloading, "Downloading video", 5)
	path, err := m.tools.Download(ctx, job.url, job.dir, func(progress int) {
		job.setProgress(5 + progress*70/100)
	})
	if err != nil {
		job.fail(err)
		return
	}

	job.set(StateProcessing, "Checking video", 78)
	info, err := m.tools.ProbeFile(ctx, path)
	if err != nil {
		job.fail(err)
		return
	}
	if info.Duration > 0 {
		job.setMeta(meta.Title, info.Duration)
	}
	sourceBytes := info.Size
	job.setSourceBytes(sourceBytes)

	finalPath := path
	finalInfo := info
	switch {
	case job.maxBytes > 0 && info.Size > job.maxBytes:
		job.set(StateProcessing, fmt.Sprintf("Compressing to %s", formatBytes(job.maxBytes)), 80)
		dst := filepath.Join(job.dir, "final.mp4")
		out, err := m.tools.Transcode(ctx, path, dst, info, job.maxBytes, func(progress float64) {
			job.setProgress(80 + int(progress*19))
		})
		if err != nil {
			job.fail(err)
			return
		}
		_ = os.Remove(path)
		finalPath, finalInfo = dst, out
	case !info.BrowserPlayable():
		job.set(StateProcessing, "Preparing video for playback", 80)
		dst := filepath.Join(job.dir, "final.mp4")
		out, err := m.tools.Transcode(ctx, path, dst, info, 0, func(progress float64) {
			job.setProgress(80 + int(progress*19))
		})
		if err != nil {
			job.fail(err)
			return
		}
		_ = os.Remove(path)
		finalPath, finalInfo = dst, out
	}

	job.complete(finalPath, finalInfo, sourceBytes)
}

func (m *Manager) Get(id string) (JobView, bool) {
	m.mu.RLock()
	job := m.jobs[id]
	m.mu.RUnlock()
	if job == nil {
		return JobView{}, false
	}
	return job.snapshot(), true
}

func (m *Manager) Media(id string) (path, name string, modTime time.Time, ok bool) {
	m.mu.RLock()
	job := m.jobs[id]
	m.mu.RUnlock()
	if job == nil {
		return "", "", time.Time{}, false
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	if job.state != StateDone || job.filePath == "" {
		return "", "", time.Time{}, false
	}
	return job.filePath, job.fileName, job.finishedAt, true
}

// Cancel stops an active job. For a finished job it removes the job and its
// media file immediately.
func (m *Manager) Cancel(id string) bool {
	m.mu.RLock()
	job := m.jobs[id]
	m.mu.RUnlock()
	if job == nil {
		return false
	}

	job.cancel()
	if job.isFinished() {
		m.removeJob(id)
		return true
	}
	job.setPhase("Canceling…")
	return true
}

func (m *Manager) removeJob(id string) {
	m.mu.Lock()
	job := m.jobs[id]
	delete(m.jobs, id)
	m.mu.Unlock()
	if job != nil {
		_ = os.RemoveAll(job.dir)
	}
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.cleanupExpired()
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Manager) cleanupExpired() {
	m.mu.Lock()
	var dirs []string
	for id, job := range m.jobs {
		job.mu.Lock()
		expired := (job.state == StateDone || job.state == StateError || job.state == StateCanceled) &&
			!job.finishedAt.IsZero() && time.Since(job.finishedAt) > m.cfg.JobTTL
		dir := job.dir
		job.mu.Unlock()
		if expired {
			delete(m.jobs, id)
			dirs = append(dirs, dir)
		}
	}
	m.mu.Unlock()

	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.stop()
		done := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			m.log.Warn("timed out waiting for jobs to stop")
		}
	})
}

func removeOrphanJobDirs(dataDir string, logger *slog.Logger) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isJobID(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dataDir, entry.Name())); err != nil {
			logger.Warn("couldn't remove leftover job directory", "dir", entry.Name(), "error", err)
		}
	}
}

func newJobID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("couldn't create a job id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func isJobID(name string) bool {
	if len(name) != 16 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func safeFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "video"
	}
	title = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '"', '<', '>', ':', '|', '?', '*':
			return '-'
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "video"
	}
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	return title + ".mp4"
}
