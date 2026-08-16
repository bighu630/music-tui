//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || illumos

package singleinstance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireSuccess：正常获取锁，锁文件存在，Close 无错。
func TestAcquireSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire 应成功: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("锁文件应存在: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close 应无错: %v", err)
	}
	// unix 设计行为：Close 不删除锁文件（避免解锁-删除窗口期 inode
	// 竞态），残留文件由下次启动 O_CREATE 复用。
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Close 后锁文件应保留: %v", err)
	}
}

// TestAcquireConflictWhileHeld：锁 A 持有中，同路径二次 Acquire 报 ErrInstanceRunning。
func TestAcquireConflictWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	lockA, err := Acquire(path)
	if err != nil {
		t.Fatalf("首次 Acquire 应成功: %v", err)
	}
	defer lockA.Close()

	_, err = Acquire(path)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("持有中二次获取应报 ErrInstanceRunning, got: %v", err)
	}
}

// TestAcquireAfterRelease：Close 后再次 Acquire 成功；再获取第二次仍成功，证明锁已释放。
func TestAcquireAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("首次 Acquire 应成功: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
	for i := 0; i < 2; i++ {
		l, err := Acquire(path)
		if err != nil {
			t.Fatalf("释放后第 %d 次 Acquire 应成功（锁未释放?）: %v", i+1, err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("Close 应无错: %v", err)
		}
	}
}

// TestAcquireErrorOnMissingDir：路径目录不存在 → 返回错误且非 ErrInstanceRunning。
func TestAcquireErrorOnMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "music-tui.lock")
	_, err := Acquire(path)
	if err == nil {
		t.Fatal("目录不存在时应返回错误")
	}
	if errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("打开锁文件失败不应是 ErrInstanceRunning: %v", err)
	}
}
