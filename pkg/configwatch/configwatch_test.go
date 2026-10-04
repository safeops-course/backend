package configwatch

import (
	"testing"
	"time"

	"github.com/ldbl/sre/backend/pkg/logger"
	"go.uber.org/zap"
)

// After Close the loop must return, not spin on the closed channels.
func TestLoopReturnsAfterClose(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), logger.Wrap(zap.NewNop()))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		w.loop()
		close(done)
	}()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not return after Close (it spins on the closed channels)")
	}
}
