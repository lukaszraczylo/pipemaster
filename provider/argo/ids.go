package argo

import "github.com/lukaszraczylo/pipemaster/provider/internal/dnsid"

// SanitizeID makes id a DNS-1123 label of at most 63 bytes; see dnsid.Sanitize.
func (adapter) SanitizeID(id string) string { return dnsid.Sanitize(id) }
