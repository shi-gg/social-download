package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	probeTimeout   = 2 * time.Minute
	maxVideoHeight = 1080
)

// formatSelector prefers an H.264/AAC MP4 so the browser can play it without
// another encode, but always has a fallback for sites that only offer webm/av1.
const formatSelector = "bv*[height<=1080][vcodec^=avc1]+ba[acodec^=mp4a]/bv*[height<=1080][ext=mp4]+ba[ext=m4a]/bv*[height<=1080]+ba/b[height<=1080]/b"

type VideoMeta struct {
	Title          string  `json:"title"`
	Duration       float64 `json:"duration"`
	Filesize       int64   `json:"filesize"`
	FilesizeApprox int64   `json:"filesize_approx"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Ext            string  `json:"ext"`
	IsLive         bool    `json:"is_live"`
	LiveStatus     string  `json:"live_status"`
	Type           string  `json:"_type"`
	WebpageURL     string  `json:"webpage_url"`
}

func (t Tools) Probe(ctx context.Context, url string) (VideoMeta, error) {
	args := t.baseArgs()
	args = append(args, "-J", "--no-progress", "--", url)
	out, err := runForOutput(ctx, t.YTDLP, probeTimeout, args...)
	if err != nil {
		return VideoMeta{}, err
	}

	var meta VideoMeta
	if err := json.Unmarshal(out, &meta); err != nil {
		return VideoMeta{}, fmt.Errorf("couldn't read video details: %w", err)
	}
	if meta.Type == "playlist" {
		return VideoMeta{}, errors.New("playlists aren't supported — paste a single video link")
	}
	return meta, nil
}

// Download fetches one video into dir and returns the media file. Each job has
// its own directory, so finding the output is simply the largest media file.
func (t Tools) Download(ctx context.Context, url, dir string, onProgress func(int)) (string, error) {
	args := t.baseArgs()
	args = append(args,
		"--newline",
		"--progress",
		"--progress-template", "download:%(progress._percent_str)s",
		"--concurrent-fragments", "4",
		"--max-filesize", strconv.FormatInt(t.MaxDownloadBytes, 10),
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"-o", filepath.Join(dir, "source.%(ext)s"),
		"--", url,
	)

	percentPattern := regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*%`)
	if err := runStreaming(ctx, t.YTDLP, func(line string) {
		match := percentPattern.FindStringSubmatch(ansiPattern.ReplaceAllString(line, ""))
		if match == nil || onProgress == nil {
			return
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return
		}
		onProgress(clampPercent(int(value)))
	}, args...); err != nil {
		return "", err
	}

	path, size, err := largestMediaFile(dir)
	if err != nil {
		return "", err
	}
	if size <= 0 {
		return "", errors.New("the download produced an empty file")
	}
	if size > t.MaxDownloadBytes {
		return "", fmt.Errorf("the video is larger than the %s download limit", formatBytes(t.MaxDownloadBytes))
	}
	return path, nil
}

func (t Tools) baseArgs() []string {
	args := []string{
		"--no-playlist",
		"--playlist-items", "1",
		"--no-warnings",
		"--socket-timeout", "20",
		"--retries", "3",
		"--extractor-retries", "3",
		"--fragment-retries", "3",
	}
	if t.Cookies != "" {
		args = append(args, "--cookies", t.Cookies)
	}
	return args
}

func largestMediaFile(dir string) (string, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, fmt.Errorf("couldn't inspect the download directory: %w", err)
	}

	var bestPath string
	var bestSize int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".mp4", ".m4v", ".mov", ".webm", ".mkv", ".flv":
		default:
			continue
		}
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() > bestSize {
			bestPath, bestSize = filepath.Join(dir, name), info.Size()
		}
	}
	if bestPath == "" {
		return "", 0, errors.New("no video file was downloaded")
	}
	return bestPath, bestSize, nil
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
