package users

import (
	"fmt"
	"strings"
	"time"
)

// deviceLabel turns a User-Agent into "Chrome on Android". Deliberately
// small: it only needs to help people recognise their own devices.
func deviceLabel(ua string) (label string, mobile bool) {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "SamsungBrowser"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(ua, "Android"):
		os, mobile = "Android", true
	case strings.Contains(ua, "iPhone"):
		os, mobile = "iPhone", true
	case strings.Contains(ua, "iPad"):
		os, mobile = "iPad", true
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		os = "Mac"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if ua == "" {
		return "Unknown device", false
	}
	if os == "" {
		return browser, mobile
	}
	return browser + " on " + os, mobile
}

// ago renders a coarse relative time: "Active now", "12 min ago", "3 days ago".
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 5*time.Minute:
		return "Active now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "Yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
