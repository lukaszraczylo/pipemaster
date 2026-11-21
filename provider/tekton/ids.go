package tekton

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	// maxName is the longest Kubernetes object or Tekton task name: a DNS-1123 label.
	maxName    = 63
	fallbackID = "default"
	// hashLen is the number of hex digits of the digest that keeps truncated names apart.
	hashLen = 8
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// SanitizeID makes id a DNS-1123 label: lowercase letters, digits and dashes, at most 63 bytes. A name that
// needs truncation ends in a dash and a digest of the whole id, so two long ids with one prefix stay apart.
func (adapter) SanitizeID(id string) string {
	if len(id) <= maxName && dnsLabel.MatchString(id) {
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
	if len(out) > maxName {
		sum := sha256.Sum256([]byte(id))
		keep := maxName - hashLen - 1
		out = strings.TrimRight(out[:keep], "-") + "-" + hex.EncodeToString(sum[:])[:hashLen]
	}
	if out == "" {
		return fallbackID
	}
	return out
}
