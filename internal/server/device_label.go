package server

import (
	"regexp"
	"strings"
)

// DeviceLabel turns a raw User-Agent string into a short human-readable
// label ("Firefox 130 on Linux") for the session list (docs/32 §3.5). It is
// best-effort display metadata only - never used for authorization - and
// degrades to "Unknown device" rather than leaking the raw UA into the UI.

type devicePattern struct {
	re      *regexp.Regexp
	name    string
	keepVer bool
}

// Browser patterns are ordered: the Edge/Opera tokens ride inside Chrome
// user agents and must win over the Chrome match; Safari is matched through
// its Version/ token because the Safari/ token is also spoofed by Chrome.
var browserPatterns = []devicePattern{
	{regexp.MustCompile(`(?:Edg|EdgA|EdgiOS)/(\d+)`), "Edge", true},
	{regexp.MustCompile(`OPR/(\d+)`), "Opera", true},
	{regexp.MustCompile(`FxiOS/(\d+)`), "Firefox", true},
	{regexp.MustCompile(`Firefox/(\d+)`), "Firefox", true},
	{regexp.MustCompile(`CriOS/(\d+)`), "Chrome", true},
	{regexp.MustCompile(`Chrome/(\d+)`), "Chrome", true},
	{regexp.MustCompile(`Version/(\d+)[^)]*Safari`), "Safari", true},
	{regexp.MustCompile(`curl/([\d.]+)`), "curl", true},
	{regexp.MustCompile(`Wget/([\d.]+)`), "wget", true},
	{regexp.MustCompile(`python-requests/([\d.]+)`), "python-requests", true},
}

// DeviceLabel renders the display label for a session's user agent.
func DeviceLabel(userAgent string) string {
	browser, version := parseBrowser(userAgent)
	osName := parseOS(userAgent)

	switch {
	case browser == "" && osName == "":
		return "Unknown device"
	case browser == "":
		return "Unknown device on " + osName
	case osName == "":
		return browser + " " + version
	default:
		return browser + " " + version + " on " + osName
	}
}

func parseBrowser(userAgent string) (string, string) {
	for _, p := range browserPatterns {
		if m := p.re.FindStringSubmatch(userAgent); m != nil {
			if p.keepVer {
				return p.name, m[1]
			}
			return p.name, ""
		}
	}
	return "", ""
}

func parseOS(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "iPhone") || strings.Contains(userAgent, "iPad") || strings.Contains(userAgent, "iPod"):
		return "iOS"
	case strings.Contains(userAgent, "Android"):
		return "Android"
	case strings.Contains(userAgent, "Windows"):
		return "Windows"
	case strings.Contains(userAgent, "Mac OS X") || strings.Contains(userAgent, "Macintosh"):
		return "macOS"
	case strings.Contains(userAgent, "CrOS"):
		return "ChromeOS"
	case strings.Contains(userAgent, "Linux") || strings.Contains(userAgent, "X11"):
		return "Linux"
	default:
		return ""
	}
}
