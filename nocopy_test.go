package crema

import (
	"sync"
	"testing"
)

var _ sync.Locker = (*noCopy)(nil)

func TestNoCopy_LockUnlock(t *testing.T) {
	t.Parallel()

	var noCopy noCopy
	noCopy.Lock()
	noCopy.Unlock()
}
