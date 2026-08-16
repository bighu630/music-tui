package singleinstance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestPidLockConflict：锁文件内容为存活 pid（本进程）→ 报 ErrInstanceRunning。
func TestPidLockConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := acquirePidLock(path)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("存活 pid 的锁文件应报 ErrInstanceRunning, got: %v", err)
	}
}

// TestPidLockStaleCleaned：锁文件内容为已死 pid → 按陈旧处理，获取成功，
// 且文件内容被改写为本进程 pid。
func TestPidLockStaleCleaned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	if err := os.WriteFile(path, []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := processAlive
	processAlive = func(pid int) bool { return false }
	t.Cleanup(func() { processAlive = orig })

	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("陈旧锁应被清理并获取成功: %v", err)
	}
	defer l.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != strconv.Itoa(os.Getpid()) {
		t.Errorf("锁文件内容 = %q, want 本进程 pid %d", data, os.Getpid())
	}
}

// TestPidLockCorruptContent：锁文件内容损坏 → 按陈旧处理，获取成功。
func TestPidLockCorruptContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("损坏内容应视为陈旧锁并获取成功: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
}

// TestPidLockReleaseAndReacquire：获取 → Close（文件应被删除）→ 再获取成功。
func TestPidLockReleaseAndReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Close 后锁文件应被删除, stat err: %v", err)
	}
	l2, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("释放后再次获取应成功: %v", err)
	}
	if err := l2.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
}

// TestPidLockCloseKeepsForeignLock：获取锁 A 后把文件内容改写为其他 pid，
// Close(A) 不得删除该文件（防 pid 重用误删他人锁）。
func TestPidLockCloseKeepsForeignLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	// 模拟 pid 重用：文件内容被其他实例改写
	if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Close 不应删除他人锁文件: %v", err)
	}
}

// TestPidLockAcquireErrorOnMissingDir：目录不存在 → 返回错误且非 ErrInstanceRunning。
func TestPidLockAcquireErrorOnMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "music-tui.lock")
	_, err := acquirePidLock(path)
	if err == nil {
		t.Fatal("目录不存在时应返回错误")
	}
	if errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("创建锁文件失败不应是 ErrInstanceRunning: %v", err)
	}
}
