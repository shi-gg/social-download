# Social Download

A small Go 1.27 server with a black, single-page UI that downloads a video from
a social link with [yt-dlp](https://github.com/yt-dlp/yt-dlp), optionally
compresses it to a target size with FFmpeg, and plays it back in the browser.

Paste a link, pick "Original quality" or a maximum size (for example 8 MB), and
hit **Download**. The page shows the video's duration, dimensions, and file
size, and gives you a save link.

## Quick start

```sh
docker compose up --build
```

Open <http://localhost:8080>. Set `HOST_PORT` if 8080 is taken:

```sh
HOST_PORT=18081 docker compose up --build
```

The container includes yt-dlp, FFmpeg, and Deno (which recent yt-dlp versions
use for YouTube's JavaScript challenges). Downloads are kept in the `media`
volume and cleaned up after six hours.

## What it supports

The URL allow-list covers YouTube (including short links), X/Twitter, TikTok,
Instagram, Facebook, Reddit, Vimeo, Twitch, Dailymotion, Streamable, Pinterest,
Snapchat, LinkedIn, Bluesky, Threads, Tumblr, Kick, Rumble, Odysee, Bilibili,
Niconico, VK, Loom, and a few more. Single videos only; live streams, playlists,
and sites with a login wall may not work.

This is intended for personal use. Respect each platform's terms and the
creator's rights.

## How the size target works

1. yt-dlp probes the link and downloads a browser-friendly MP4 (H.264/AAC where
   available), capped at 1080p.
2. FFprobe reads the real duration, dimensions, and size of the file.
3. If the file is larger than the requested maximum, FFmpeg runs a two-pass
   H.264 encode. The video bitrate is calculated from the target size and
   duration, audio is reduced to 32–96 kbps, and the resolution is lowered for
   very small targets. The result is verified against the target and re-encoded
   with a lower bitrate if it overshoots.
4. If the file is already under the target, it is served unchanged. Files that
   are not browser-playable are converted to MP4 first.

A target that is too small for the video's duration gets a clear error with the
minimum size that would work.

## Running locally

Requirements: Go 1.27, yt-dlp, ffmpeg, and ffprobe on `PATH`.

```sh
go run ./cmd/server
```

Then open <http://localhost:8080>. Downloads go to `./data`.

## Tests

```sh
go test ./...
```

The integration test generates a short video with FFmpeg, uses a fake yt-dlp
that copies it into the job directory, and exercises the real HTTP API,
compression pipeline, and media range requests. It skips if ffmpeg is not
installed.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `ADDR` | `:8080` | Listen address |
| `DATA_DIR` | `./data` | Job/media storage directory |
| `YTDLP_BIN` | `yt-dlp` | yt-dlp executable |
| `FFMPEG_BIN` | `ffmpeg` | FFmpeg executable |
| `FFPROBE_BIN` | `ffprobe` | FFprobe executable |
| `YTDLP_COOKIES_FILE` | empty | Optional cookies file for yt-dlp |
| `MAX_DOWNLOAD_MB` | `1024` | Maximum source download size |
| `MAX_DURATION_MINUTES` | `120` | Maximum video duration |
| `JOB_TIMEOUT_MINUTES` | `30` | Per-job time limit |
| `JOB_TTL_MINUTES` | `360` | How long finished jobs/media are kept |
| `MAX_CONCURRENT_JOBS` | `2` | Simultaneous downloads/encodes |
| `MAX_JOBS` | `50` | Maximum jobs retained at once |

Target sizes are limited to 1 MB–8192 MB. The UI offers 4–128 MB presets plus a
custom value.

## API

- `POST /api/jobs` — `{"url": "...", "maxBytes": 8388608}`; returns `202` with
  a job. `maxBytes: 0` means original quality.
- `GET /api/jobs/{id}` — job state, progress, metadata, and `mediaUrl`.
- `DELETE /api/jobs/{id}` — cancel an active job or delete a finished one.
- `GET /media/{id}` — the MP4, with HTTP range support.
- `GET /api/health` — health check.

## Container image

The GitHub Actions workflow in `.github/workflows/build.yml` runs vet/tests and
builds a multi-arch (`linux/amd64`, `linux/arm64`) image. On pushes to `main`
and tags it publishes to GHCR:

```sh
docker pull ghcr.io/<owner>/<repo>:latest
```

If you use the published image, replace `build: .` in `docker-compose.yml` with
`image: ghcr.io/<owner>/<repo>:latest`.
