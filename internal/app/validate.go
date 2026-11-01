package app

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// supportedDomains is intentionally an allow-list. yt-dlp will fetch whatever
// URL it is given, so we only let it talk to sites this app is meant for.
var supportedDomains = []string{
	"youtube.com", "youtu.be", "youtube-nocookie.com",
	"twitter.com", "x.com",
	"tiktok.com",
	"instagram.com",
	"facebook.com", "fb.watch",
	"reddit.com", "redd.it",
	"vimeo.com",
	"twitch.tv",
	"dailymotion.com", "dai.ly",
	"streamable.com",
	"pinterest.com",
	"snapchat.com",
	"linkedin.com",
	"bsky.app",
	"threads.net",
	"tumblr.com",
	"kick.com",
	"rumble.com",
	"odysee.com",
	"bitchute.com",
	"coub.com",
	"imgur.com",
	"bilibili.com", "b23.tv",
	"nicovideo.jp", "nico.ms",
	"vk.com", "ok.ru",
	"loom.com",
	"9gag.com",
	"ifunny.co",
	"archive.org",
}

var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

const (
	maxURLLength   = 4096
	supportedSites = "YouTube, X/Twitter, TikTok, Instagram, Facebook, Reddit, Vimeo, Twitch, Dailymotion, and more"
)

// ValidateURL cleans and validates a pasted link. It accepts links embedded in
// share text (the common "check this out https://..." paste), upgrades http to
// https, and rejects anything outside the supported social/video sites.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("paste a video link first")
	}
	if len(raw) > maxURLLength {
		return "", errors.New("that link is too long")
	}

	candidate := raw
	hasWhitespace := strings.ContainsAny(raw, " \t\r\n")
	if !hasHTTPScheme(candidate) || hasWhitespace {
		if match := urlPattern.FindString(raw); match != "" {
			candidate = match
		} else if !hasWhitespace {
			// Let people paste "youtu.be/abc" without the scheme.
			candidate = "https://" + raw
		} else {
			return "", errors.New("that doesn't look like a link — include https://")
		}
	}
	candidate = strings.TrimRight(candidate, ".,;:!?)]}\"'")

	u, err := url.Parse(candidate)
	if err != nil {
		return "", errors.New("that doesn't look like a valid link")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("only http and https links are supported")
	}
	if u.User != nil {
		return "", errors.New("links with a username or password aren't supported")
	}
	if u.Port() != "" {
		return "", errors.New("links with an explicit port aren't supported")
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", errors.New("that link is missing a website address")
	}
	if ip := net.ParseIP(host); ip != nil {
		return "", errors.New("IP addresses aren't supported — paste a link from a supported site")
	}
	if !isSupportedHost(host) {
		return "", fmt.Errorf("that site isn't supported yet. Try %s", supportedSites)
	}
	if u.Path == "" {
		u.Path = "/"
	}

	// Always use TLS for the sites we support; it also keeps redirects sane.
	u.Scheme = "https"
	u.Fragment = ""
	u.RawFragment = ""

	return u.String(), nil
}

func hasHTTPScheme(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func isSupportedHost(host string) bool {
	for _, domain := range supportedDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
