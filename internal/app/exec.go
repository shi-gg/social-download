package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxToolOutput = 64 << 10

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		copy(b.buf, b.buf[len(b.buf)-b.max:])
		b.buf = b.buf[:b.max]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureCommand(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// runForOutput runs a command and returns stdout. stderr is kept only for a
// useful error message, so failed commands never dump huge logs into the API.
func runForOutput(ctx context.Context, name string, timeout time.Duration, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout bytes.Buffer
	stderr := newTailBuffer(maxToolOutput)
	cmd := newCommand(runCtx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err != nil {
		return nil, commandError(ctx, runCtx, name, timeout, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runStreaming runs a command and sends each stdout line to onLine. Both
// yt-dlp progress output and ffmpeg -progress output are line based, which
// keeps this small and avoids screenscraping carriage-return updates.
func runStreaming(ctx context.Context, name string, onLine func(string), args ...string) error {
	stderr := newTailBuffer(maxToolOutput)
	cmd := newCommand(ctx, name, args...)
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start %s: %w", name, err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return toolError(name, waitErr, stderr.String())
	}
	if scanErr != nil {
		return fmt.Errorf("%s output could not be read: %w", name, scanErr)
	}
	return nil
}

func commandError(ctx, runCtx context.Context, name string, timeout time.Duration, err error, stderr string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out after %s", name, durationShort(timeout))
	}
	return toolError(name, err, stderr)
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func toolError(name string, err error, stderr string) error {
	detail := lastErrorLine(stderr)
	if detail == "" {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "video unavailable"), strings.Contains(lower, "video is unavailable"):
		return errors.New("this video is unavailable")
	case strings.Contains(lower, "private video"), strings.Contains(lower, "video is private"):
		return errors.New("this video is private")
	case strings.Contains(lower, "sign in to confirm"):
		return errors.New("the site wants a sign-in from this server — try again later or configure YTDLP_COOKIES_FILE")
	case strings.Contains(lower, "unsupported url"):
		return errors.New("this link isn't supported")
	case strings.Contains(lower, "larger than max-filesize"):
		return errors.New("this video is larger than the download limit")
	case strings.Contains(lower, "requested format is not available"):
		return errors.New("no compatible video format was available")
	case strings.Contains(lower, "http error 404"):
		return errors.New("the video was not found (404)")
	}
	return fmt.Errorf("%s: %s", name, detail)
}

func lastErrorLine(stderr string) string {
	stderr = ansiPattern.ReplaceAllString(stderr, "")
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	var last, lastError string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		last = line
		if strings.Contains(strings.ToUpper(line), "ERROR") {
			lastError = line
		}
	}
	if lastError != "" {
		last = lastError
	}
	last = strings.TrimSpace(strings.TrimPrefix(last, "ERROR:"))
	last = strings.TrimSpace(strings.TrimPrefix(last, "Error:"))
	if len(last) > 300 {
		last = last[:300] + "…"
	}
	return last
}

func formatBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	if value >= 100 {
		return fmt.Sprintf("%.0f %cB", value, "KMGT"[exp])
	}
	return fmt.Sprintf("%.1f %cB", value, "KMGT"[exp])
}

func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "unknown length"
	}
	total := int64(seconds + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func durationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}
