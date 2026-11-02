[![Build](https://github.com/shi-gg/social-download/actions/workflows/build.yml/badge.svg)](https://github.com/shi-gg/social-download/actions/workflows/build.yml)
![](https://img.shields.io/github/repo-size/shi-gg/social-download?maxAge=3600)
![](https://img.shields.io/badge/ghcr.io-shi--gg%2Fsocial--download-blue)

## About
Social Download is a small Go 1.27 server with a black, single-page UI that downloads a video from a social link with [yt-dlp](https://github.com/yt-dlp/yt-dlp), optionally compresses it to a target size with FFmpeg, and plays it back in the browser.

Paste a link, pick "Original quality" or a maximum size (for example 8 MB), and hit **Download**. The page shows the video's duration, dimensions, and file size, and gives you a save link. Downloads run as background jobs with progress, cancel support, and automatic cleanup.

**You can use the following sources**
- YouTube (including short links)
- X/Twitter
- TikTok
- Instagram
- Facebook
- Reddit
- Vimeo
- Twitch
- Dailymotion, Streamable, Pinterest, Snapchat
- LinkedIn, Bluesky, Threads, Tumblr, Kick, Rumble, Odysee, Bilibili, Niconico, VK, Loom

The URL allow-list is enforced server-side, so the server never fetches arbitrary links. Single videos only; live streams, playlists, and sites with a login wall may not work.
<br />

## Setup & Usage
Social Download ships as a Docker image with yt-dlp, FFmpeg, and Deno (which recent yt-dlp versions use for YouTube's JavaScript challenges) already installed.

### Docker Deployment
The recommended way to run it is with Compose. Create a `compose.yml`:

```yml
services:
    social-download:
        image: ghcr.io/shi-gg/social-download:latest
        container_name: social-download
        restart: unless-stopped
        ports:
            - "8080:8080"
        environment:
            DATA_DIR: /data
            MAX_DOWNLOAD_MB: "1024"
            MAX_DURATION_MINUTES: "120"
            MAX_CONCURRENT_JOBS: "2"
            # Optional: require this password for all /api requests.
            DOWNLOAD_PASSWORD: ${DOWNLOAD_PASSWORD:-}
        volumes:
            - social-download-data:/data
        logging:
            driver: json-file
            options:
                max-size: "10m"
                max-file: "3"

volumes:
    social-download-data:
```

To start the server, run:
```bash
docker compose up -d
```

Open <http://localhost:8080>. If port 8080 is taken, change the port mapping (for example `"18081:8080"`).

To update the server, pull the latest image and recreate the container:
```bash
docker compose pull
docker compose up -d
```

The image is built for `linux/amd64` and `linux/arm64`. Downloads are kept in the `social-download-data` volume and cleaned up after six hours.

### Local Development
Requirements: Go 1.27, yt-dlp, ffmpeg, and ffprobe on `PATH`.

```bash
go run ./cmd/server
```

Then open <http://localhost:8080>. Downloads go to `./data`.

To build the container from source instead of using the published image:

```bash
docker compose up --build
```

## Configuration
The following env variables can be set for the server.

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:8080` | Listen address |
| `DATA_DIR` | `./data` | Job/media storage directory |
| `YTDLP_BIN` | `yt-dlp` | yt-dlp executable |
| `FFMPEG_BIN` | `ffmpeg` | FFmpeg executable |
| `FFPROBE_BIN` | `ffprobe` | FFprobe executable |
| `YTDLP_COOKIES_FILE` | — | Optional cookies file for yt-dlp |
| `DOWNLOAD_PASSWORD` | — | Optional password required for every `/api` request; leave unset or empty to disable |
| `MAX_DOWNLOAD_MB` | `1024` | Maximum source download size |
| `MAX_DURATION_MINUTES` | `120` | Maximum video duration |
| `JOB_TIMEOUT_MINUTES` | `30` | Per-job time limit |
| `JOB_TTL_MINUTES` | `360` | How long finished jobs/media are kept |
| `MAX_CONCURRENT_JOBS` | `2` | Simultaneous downloads/encodes |
| `MAX_JOBS` | `50` | Maximum jobs retained at once |

Target sizes are limited to 1 MB–8192 MB. The UI offers 4–128 MB presets plus a custom value.

## How the size target works
1. yt-dlp probes the link and downloads a browser-friendly MP4 (H.264/AAC where available), capped at 1080p.
2. FFprobe reads the real duration, dimensions, and size of the file.
3. If the file is larger than the requested maximum, FFmpeg runs a two-pass H.264 encode. The video bitrate is calculated from the target size and duration, audio is reduced to 32–96 kbps, and the resolution is lowered for very small targets. The result is verified against the target and re-encoded with a lower bitrate if it overshoots.
4. If the file is already under the target, it is served unchanged. Files that are not browser-playable are converted to MP4 first.

A target that is too small for the video's duration gets a clear error with the minimum size that would work.

## API
- `POST /api/jobs` — `{"url": "...", "maxBytes": 8388608}`; returns `202` with a job. `maxBytes: 0` means original quality.
- `GET /api/jobs/{id}` — job state, progress, metadata, and `mediaUrl`.
- `DELETE /api/jobs/{id}` — cancel an active job or delete a finished one.
- `GET /media/{id}` — the MP4, with HTTP range support.
- `GET /api/health` — health check.

When `DOWNLOAD_PASSWORD` is set, send it in the `X-Download-Password` header for every `/api` request. The `/media/{id}` URL remains public so video playback, saving, and copied media URLs continue to work without the password.

## Development
The GitHub Actions workflow in `.github/workflows/build.yml` runs `go vet` and `go test`, then builds and publishes a multi-arch (`linux/amd64`, `linux/arm64`) image. Pushes to `main` publish `latest` and a SHA tag, while `v*` tags also publish semantic version tags:

```bash
docker pull ghcr.io/shi-gg/social-download:latest
```

This is intended for personal use. Respect each platform's terms and the creator's rights.
