package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ffprobeTimeout = 30 * time.Second
	minVideoKbps   = 30
	maxVideoKbps   = 50000
)

type Tools struct {
	YTDLP            string
	FFmpeg           string
	FFprobe          string
	Cookies          string
	MaxDownloadBytes int64
}

type MediaInfo struct {
	Duration    float64
	Size        int64
	FormatName  string
	VideoCodec  string
	AudioCodec  string
	PixelFormat string
	Width       int
	Height      int
}

func (m MediaInfo) BrowserPlayable() bool {
	if !strings.Contains(m.FormatName, "mp4") && !strings.Contains(m.FormatName, "mov") {
		return false
	}
	if m.VideoCodec != "h264" {
		return false
	}
	if m.PixelFormat != "yuv420p" && m.PixelFormat != "yuvj420p" {
		return false
	}
	if m.AudioCodec != "" && m.AudioCodec != "aac" {
		return false
	}
	return m.Width > 0 && m.Height > 0
}

func (t Tools) ProbeFile(ctx context.Context, path string) (MediaInfo, error) {
	out, err := runForOutput(ctx, t.FFprobe, ffprobeTimeout,
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return MediaInfo{}, err
	}

	var parsed struct {
		Format struct {
			Duration   string `json:"duration"`
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			PixFmt    string `json:"pix_fmt"`
			Duration  string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return MediaInfo{}, fmt.Errorf("couldn't read the downloaded video: %w", err)
	}

	info := MediaInfo{FormatName: parsed.Format.FormatName}
	info.Duration, _ = strconv.ParseFloat(parsed.Format.Duration, 64)
	for _, stream := range parsed.Streams {
		switch stream.CodecType {
		case "video":
			if info.VideoCodec == "" {
				info.VideoCodec = stream.CodecName
				info.Width, info.Height = stream.Width, stream.Height
				info.PixelFormat = stream.PixFmt
				if info.Duration <= 0 {
					info.Duration, _ = strconv.ParseFloat(stream.Duration, 64)
				}
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = stream.CodecName
			}
		}
	}
	if info.VideoCodec == "" {
		return MediaInfo{}, errors.New("the downloaded file doesn't contain a video")
	}
	stat, err := os.Stat(path)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("couldn't read the downloaded video: %w", err)
	}
	info.Size = stat.Size()
	return info, nil
}

// Transcode makes a browser-friendly MP4. When maxBytes > 0 it encodes to fit
// that size using a two-pass bitrate calculation; otherwise it uses CRF.
func (t Tools) Transcode(ctx context.Context, src, dst string, info MediaInfo, maxBytes int64, onProgress func(float64)) (MediaInfo, error) {
	tmp := dst + ".tmp.mp4"
	_ = os.Remove(tmp)
	defer os.Remove(tmp)

	logBase := filepath.Join(filepath.Dir(dst), "ffpass")
	defer removePassLogs(logBase)

	if maxBytes > 0 {
		videoKbps, audioKbps, err := planBitrate(maxBytes, info.Duration)
		if err != nil {
			return MediaInfo{}, err
		}

		for attempt := 1; ; attempt++ {
			if err := t.encodeTarget(ctx, src, tmp, info, videoKbps, audioKbps, logBase, onProgress); err != nil {
				return MediaInfo{}, err
			}
			stat, err := os.Stat(tmp)
			if err != nil {
				return MediaInfo{}, fmt.Errorf("couldn't read the compressed video: %w", err)
			}
			if stat.Size() <= maxBytes {
				break
			}
			if attempt >= 3 {
				return MediaInfo{}, fmt.Errorf("couldn't fit the video into %s — try a larger target", formatBytes(maxBytes))
			}
			// Aim a little below the limit on the next attempt.
			ratio := float64(maxBytes) / float64(stat.Size())
			next := int(float64(videoKbps) * ratio * 0.97)
			if next < minVideoKbps {
				return MediaInfo{}, fmt.Errorf("couldn't fit the video into %s — try a larger target", formatBytes(maxBytes))
			}
			videoKbps = next
		}
	} else {
		if err := t.encodeCRF(ctx, src, tmp, info, onProgress); err != nil {
			return MediaInfo{}, err
		}
	}

	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return MediaInfo{}, fmt.Errorf("couldn't save the processed video: %w", err)
	}
	outInfo, err := t.ProbeFile(ctx, dst)
	if err != nil {
		_ = os.Remove(dst)
		return MediaInfo{}, err
	}
	if maxBytes > 0 && outInfo.Size > maxBytes {
		_ = os.Remove(dst)
		return MediaInfo{}, fmt.Errorf("couldn't fit the video into %s — try a larger target", formatBytes(maxBytes))
	}
	if onProgress != nil {
		onProgress(1)
	}
	return outInfo, nil
}

func (t Tools) encodeTarget(ctx context.Context, src, dst string, info MediaInfo, videoKbps, audioKbps int, logBase string, onProgress func(float64)) error {
	videoArgs := []string{
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-b:v", fmt.Sprintf("%dk", videoKbps),
		"-maxrate", fmt.Sprintf("%dk", int(float64(videoKbps)*1.25)),
		"-bufsize", fmt.Sprintf("%dk", videoKbps*2),
	}
	if height := targetHeight(videoKbps, info.Height); height > 0 && info.Height > height {
		videoArgs = append(videoArgs, "-vf", fmt.Sprintf("scale=-2:%d", height))
	}

	pass1 := []string{
		"-hide_banner", "-nostdin", "-y", "-progress", "pipe:1", "-nostats",
		"-i", src,
		"-map", "0:v:0",
	}
	pass1 = append(pass1, videoArgs...)
	pass1 = append(pass1,
		"-pass", "1", "-passlogfile", logBase,
		"-an", "-sn", "-dn",
		"-f", "mp4", os.DevNull,
	)
	if err := runFFmpeg(ctx, t.FFmpeg, pass1, info.Duration, func(p float64) {
		if onProgress != nil {
			onProgress(p * 0.5)
		}
	}); err != nil {
		return err
	}

	pass2 := []string{
		"-hide_banner", "-nostdin", "-y", "-progress", "pipe:1", "-nostats",
		"-i", src,
		"-map", "0:v:0",
	}
	if info.AudioCodec != "" {
		pass2 = append(pass2, "-map", "0:a:0?")
	}
	pass2 = append(pass2, videoArgs...)
	pass2 = append(pass2,
		"-pass", "2", "-passlogfile", logBase,
		"-sn", "-dn",
	)
	if info.AudioCodec != "" {
		channels := 2
		if audioKbps <= 48 {
			channels = 1
		}
		pass2 = append(pass2,
			"-c:a", "aac",
			"-b:a", fmt.Sprintf("%dk", audioKbps),
			"-ac", strconv.Itoa(channels),
		)
	}
	pass2 = append(pass2, "-movflags", "+faststart", "-f", "mp4", dst)
	if err := runFFmpeg(ctx, t.FFmpeg, pass2, info.Duration, func(p float64) {
		if onProgress != nil {
			onProgress(0.5 + p*0.5)
		}
	}); err != nil {
		return err
	}
	return nil
}

func (t Tools) encodeCRF(ctx context.Context, src, dst string, info MediaInfo, onProgress func(float64)) error {
	args := []string{
		"-hide_banner", "-nostdin", "-y", "-progress", "pipe:1", "-nostats",
		"-i", src,
		"-map", "0:v:0",
	}
	if info.AudioCodec != "" {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
	)
	if info.Height > maxVideoHeight {
		args = append(args, "-vf", fmt.Sprintf("scale=-2:%d", maxVideoHeight))
	}
	if info.AudioCodec != "" {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ac", "2")
	}
	args = append(args, "-sn", "-dn", "-movflags", "+faststart", "-f", "mp4", dst)
	return runFFmpeg(ctx, t.FFmpeg, args, info.Duration, onProgress)
}

func runFFmpeg(ctx context.Context, binary string, args []string, duration float64, onProgress func(float64)) error {
	var lastReported float64
	return runStreaming(ctx, binary, func(line string) {
		var microseconds int64
		switch {
		case strings.HasPrefix(line, "out_time_us="):
			microseconds, _ = strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
		case strings.HasPrefix(line, "out_time_ms="):
			// Despite the name, ffmpeg reports microseconds here too.
			microseconds, _ = strconv.ParseInt(strings.TrimPrefix(line, "out_time_ms="), 10, 64)
		case line == "progress=end":
			if onProgress != nil {
				onProgress(1)
			}
			lastReported = 1
			return
		default:
			return
		}
		if duration <= 0 || onProgress == nil {
			return
		}
		progress := float64(microseconds) / 1e6 / duration
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		if progress > lastReported {
			lastReported = progress
			onProgress(progress)
		}
	}, args...)
}

func planBitrate(maxBytes int64, duration float64) (videoKbps, audioKbps int, err error) {
	if duration <= 0 {
		return 0, 0, errors.New("can't hit a target size because the video length is unknown")
	}
	totalKbps := float64(maxBytes) * 8 / 1000 / duration
	audioKbps = 96
	switch {
	case totalKbps < 250:
		audioKbps = 32
	case totalKbps < 450:
		audioKbps = 48
	case totalKbps < 800:
		audioKbps = 64
	}

	// Leave a little room for the MP4 container and muxing overhead.
	videoKbps = int(totalKbps*0.94) - audioKbps - 4
	if videoKbps > maxVideoKbps {
		videoKbps = maxVideoKbps
	}
	if videoKbps < minVideoKbps {
		minBytes := int64((float64(minVideoKbps+audioKbps+4) * duration * 1000 / 8) * 1.02)
		return 0, 0, fmt.Errorf("that target is too small for a %s video — choose at least %s", formatDuration(duration), formatBytes(minBytes))
	}
	return videoKbps, audioKbps, nil
}

func targetHeight(videoKbps, sourceHeight int) int {
	limit := maxVideoHeight
	switch {
	case videoKbps < 120:
		limit = 240
	case videoKbps < 250:
		limit = 360
	case videoKbps < 500:
		limit = 480
	case videoKbps < 1000:
		limit = 720
	}
	if sourceHeight > 0 && sourceHeight < limit {
		return sourceHeight
	}
	return limit
}

func removePassLogs(base string) {
	matches, _ := filepath.Glob(base + "*")
	for _, match := range matches {
		_ = os.Remove(match)
	}
}
