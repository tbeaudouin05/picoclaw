package evolution

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppendTaskRecordRespectsContextWhileWaitingForFileLock(t *testing.T) {
	paths := NewPaths(t.TempDir(), "")
	store := NewStore(paths)
	unlock := lockStoreFile(paths.TaskRecords)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.AppendTaskRecord(ctx, LearningRecord{ID: "blocked", Kind: RecordKindTask})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("AppendTaskRecord error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AppendTaskRecord did not return when its context expired while waiting for the file lock")
	}
}
