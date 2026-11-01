package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the server needs to run. All of it can be set through
// environment variables so the same binary works locally, in Compose, and in
// the container image.
type Config struct {
	Addr             string
	DataDir          string
	YTDLP            string
	FFmpeg           string
	FFprobe          string
	Cookies          string
	Version          string
	MaxDownloadBytes int64
	MaxDuration      time.Duration
	JobTimeout       time.Duration
	JobTTL           time.Duration
	MaxConcurrent    int
	MaxJobs          int
}

func DefaultConfig() Config {
	return Config{
		Addr:             ":8080",
		DataDir:          "./data",
		YTDLP:            "yt-dlp",
		FFmpeg:           "ffmpeg",
		FFprobe:          "ffprobe",
		Version:          "dev",
		MaxDownloadBytes: 1024 * 1024 * 1024,
		MaxDuration:      2 * time.Hour,
		JobTimeout:       30 * time.Minute,
		JobTTL:           6 * time.Hour,
		MaxConcurrent:    2,
		MaxJobs:          50,
	}
}

func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	cfg.Addr = envString("ADDR", cfg.Addr)
	cfg.DataDir = envString("DATA_DIR", cfg.DataDir)
	cfg.YTDLP = envString("YTDLP_BIN", cfg.YTDLP)
	cfg.FFmpeg = envString("FFMPEG_BIN", cfg.FFmpeg)
	cfg.FFprobe = envString("FFPROBE_BIN", cfg.FFprobe)
	cfg.Cookies = envString("YTDLP_COOKIES_FILE", "")

	var err error
	if cfg.MaxDownloadBytes, err = envMegabytes("MAX_DOWNLOAD_MB", cfg.MaxDownloadBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxDuration, err = envMinutes("MAX_DURATION_MINUTES", cfg.MaxDuration); err != nil {
		return cfg, err
	}
	if cfg.JobTimeout, err = envMinutes("JOB_TIMEOUT_MINUTES", cfg.JobTimeout); err != nil {
		return cfg, err
	}
	if cfg.JobTTL, err = envMinutes("JOB_TTL_MINUTES", cfg.JobTTL); err != nil {
		return cfg, err
	}
	if cfg.MaxConcurrent, err = envInt("MAX_CONCURRENT_JOBS", cfg.MaxConcurrent); err != nil {
		return cfg, err
	}
	if cfg.MaxJobs, err = envInt("MAX_JOBS", cfg.MaxJobs); err != nil {
		return cfg, err
	}

	if cfg.MaxDownloadBytes < 1<<20 {
		return cfg, fmt.Errorf("MAX_DOWNLOAD_MB must be at least 1")
	}
	if cfg.MaxDuration <= 0 || cfg.JobTimeout <= 0 || cfg.JobTTL <= 0 {
		return cfg, fmt.Errorf("duration settings must be positive")
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 32 {
		return cfg, fmt.Errorf("MAX_CONCURRENT_JOBS must be between 1 and 32")
	}
	if cfg.MaxJobs < cfg.MaxConcurrent || cfg.MaxJobs > 10000 {
		return cfg, fmt.Errorf("MAX_JOBS must be at least MAX_CONCURRENT_JOBS and at most 10000")
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return cfg, fmt.Errorf("DATA_DIR can't be empty")
	}

	return cfg, nil
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number: %w", key, err)
	}
	return v, nil
}

func envMegabytes(key string, fallbackBytes int64) (int64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallbackBytes, nil
	}
	mb, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || mb <= 0 {
		return 0, fmt.Errorf("%s must be a positive whole number of megabytes", key)
	}
	if mb > 1<<20 {
		return 0, fmt.Errorf("%s is too large", key)
	}
	return mb * 1024 * 1024, nil
}

func envMinutes(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	minutes, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || minutes <= 0 {
		return 0, fmt.Errorf("%s must be a positive whole number of minutes", key)
	}
	return time.Duration(minutes) * time.Minute, nil
}
