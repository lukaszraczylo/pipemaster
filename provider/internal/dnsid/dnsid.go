// Package dnsid makes identifiers into Kubernetes DNS-1123 labels.
package dnsid

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	// MaxName is the longest Kubernetes object name that is a DNS-1123 label.
	MaxName    = 63
	fallbackID = "default"
	// hashLen is the number of hex digits of the digest that keeps truncated names apart.
	hashLen = 8
)

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Valid reports whether id is a DNS-1123 label of at most MaxName bytes.
func Valid(id string) bool { return len(id) <= MaxName && label.MatchString(id) }

// Sanitize makes id a DNS-1123 label: lowercase letters, digits and dashes, at most 63 bytes. A name that
// needs truncation ends in a dash and a digest of the whole id, so two long ids with one prefix stay apart.
func Sanitize(id string) string {
	if Valid(id) {
		return id
	}
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallbackID
	}
	return Fit(out, id, MaxName)
}

// Fit shortens name to at most limit bytes. A shortened name ends in a dash and a digest of id.
func Fit(name, id string, limit int) string {
	if len(name) <= limit {
		return name
	}
	sum := sha256.Sum256([]byte(id))
	keep := limit - hashLen - 1
	return strings.TrimRight(name[:keep], "-") + "-" + hex.EncodeToString(sum[:])[:hashLen]
}
