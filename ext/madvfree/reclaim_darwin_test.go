//go:build darwin

package madvfree

// simulateReclaim reproduces the observable effect of kernel reclamation of an
// idle MADV_FREE_REUSABLE region: the pages read back zero-filled. Darwin's
// MADV_DONTNEED neither zeroes nor releases anonymous pages, so the test clears
// the bytes directly. MADV_FREE_REUSE then preserves these zeroes, which the
// generation check detects as a miss.
func simulateReclaim(region []byte) error {
	clear(region)

	return nil
}
