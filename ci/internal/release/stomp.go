package release

import (
	"errors"
	"fmt"
)

// checkStomp resolves the existing digest (if any) of dstRef and compares it
// against the digest of srcRef. Both must be fully-qualified references
// (host/repo:tag) — callers are responsible for prefixing a host-relative
// repo path with the destination registry's host before calling this, since
// Registry.Digest (like ListTags) never does that itself. It never mutates
// the registry.
//
// This only applies to an immutable per-version tag (promoted-{commit}-{version}
// or a production :{version} tag); a mutable "latest" pointer is expected to
// move on every run and is never checked.
//
//   - tag absent: free to write, returns ("", false, nil).
//   - tag present, same digest as src: safe no-op re-run, returns ("", true, nil)
//     so the caller can warn without blocking.
//   - tag present, different digest: a real conflict (image stomping), returns
//     a human-readable description; the caller must refuse unless forced.
func checkStomp(reg Registry, srcRef, dstRef string) (conflict string, alreadyUpToDate bool, err error) {
	srcDigest, err := reg.Digest(srcRef)
	if err != nil {
		return "", false, fmt.Errorf("resolve source %s: %w", srcRef, err)
	}

	existingDigest, err := reg.Digest(dstRef)
	if err != nil {
		if errors.Is(err, ErrTagNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve existing %s: %w", dstRef, err)
	}

	if existingDigest == srcDigest {
		return "", true, nil
	}
	return fmt.Sprintf("%s already exists at a different digest (existing=%s, new=%s)", dstRef, existingDigest, srcDigest), false, nil
}
