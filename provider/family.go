package provider

// Family returns the adapter family of id: Gitea and Forgejo are dialects of GHA.
func Family(id string) string {
	switch id {
	case Gitea, Forgejo:
		return GHA
	}
	return id
}

// SameFamily reports whether a and b share an adapter family.
func SameFamily(a, b string) bool { return Family(a) == Family(b) }
