//go:build !unix

package publish

// linkCount is unsupported here, so the store is never garbage-collected.
// TODO(M2): scan releases for referenced digests instead of relying on
// hardlink counts.
func linkCount(string) (uint64, bool) { return 0, false }
