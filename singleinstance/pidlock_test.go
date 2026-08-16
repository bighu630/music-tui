package singleinstance

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestPidLockConflict：锁文件内容为真实存活进程（辅助子进程）的 pid
// → 报 ErrInstanceRunning。
func TestPidLockConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	cmd := exec.Command(os.Args[0], "-test.run=TestPidLockHelperSleep$")
	cmd.Env = append(os.Environ(), "MUSIC_TUI_PIDLOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	// 等辅助进程输出 ready，确保其 pid 真实存活再写入锁文件
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "ready" {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("等待辅助进程就绪超时")
	}

	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = acquirePidLock(path)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("存活 pid 的锁文件应报 ErrInstanceRunning, got: %v", err)
	}
}

// TestPidLockHelperSleep 是 TestPidLockConflict 的辅助进程：输出 ready
// 后睡眠 30 秒，提供一个真实存活 pid。正常测试运行（无 env 标记）时
// 直接返回。
func TestPidLockHelperSleep(t *testing.T) {
	if os.Getenv("MUSIC_TUI_PIDLOCK_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	os.Stdout.Sync()
	time.Sleep(30 * time.Second)
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

// TestPidLockSelfPidStale：残留锁文件含本进程 pid（pid 重用场景）→
// 并发实例不可能与本进程同 pid，按陈旧处理，获取成功。
func TestPidLockSelfPidStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("含本进程 pid 的残留锁应视为陈旧并获取成功: %v", err)
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

// TestPidLockEmptyContent：锁文件内容为空（并发创建者处于 O_EXCL 创建
// 与写入之间的窗口 / 崩溃残留）→ 重读后仍为空则按陈旧处理，获取成功。
func TestPidLockEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("空内容锁文件应视为陈旧并获取成功: %v", err)
	}
	defer l.Close()
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

// TestPidLockCloseDoubleCall：Close 重复调用应安全返回 nil（文档
// 承诺的 no-op，释放后 l.f 已置 nil）。
func TestPidLockCloseDoubleCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")
	l, err := acquirePidLock(path)
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("首次 Close 应无错: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("二次 Close 应为安全 no-op, got: %v", err)
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

// TestPidLockPathIsDirectory：锁路径恰为目录（用户误配）→ 返回明确
// 错误且不删除目录。
func TestPidLockPathIsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock-dir")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := acquirePidLock(path)
	if err == nil {
		t.Fatal("锁路径为目录时应返回错误")
	}
	if errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("路径为目录不应是 ErrInstanceRunning: %v", err)
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		t.Errorf("锁路径目录应保留且未被删除, stat err: %v", err)
	}
}
