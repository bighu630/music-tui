package singleinstance

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLockHelperProcess 是跨进程测试的辅助进程：从 env 取锁路径，
// Acquire 失败 → stderr 输出错误并 exit(2)；成功 → 输出 "locked" 并
// 持锁 10 秒后正常退出。正常测试运行（无 env 标记）时直接返回。
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("MUSIC_TUI_LOCK_HELPER") != "1" {
		return
	}
	path := os.Getenv("MUSIC_TUI_LOCK_HELPER_PATH")
	lock, err := Acquire(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println("locked")
	os.Stdout.Sync()
	time.Sleep(10 * time.Second)
	lock.Close()
	os.Exit(0)
}

// TestSingleInstanceAcrossProcesses：真实跨进程互斥与崩溃后释放。
// 用 bufio 读子进程 stdout 等待 "locked" 行（不用父进程 Acquire 轮询——
// pid 方案下轮询会删子进程的锁文件，破坏性）。
func TestSingleInstanceAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "music-tui.lock")

	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		"MUSIC_TUI_LOCK_HELPER=1",
		"MUSIC_TUI_LOCK_HELPER_PATH="+path,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// 等待子进程输出 "locked" 行（带超时；提前退出则附 stderr 报错）
	locked := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "locked" {
				locked <- true
				return
			}
		}
		locked <- false
	}()
	select {
	case ok := <-locked:
		if !ok {
			t.Fatalf("helper 提前退出: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("等待 helper 锁定超时")
	}

	// 子进程持锁中，父进程同路径 Acquire 必须互斥失败
	_, err = Acquire(path)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("跨进程互斥应报 ErrInstanceRunning, got: %v", err)
	}

	// Kill 模拟崩溃（Windows 上即 TerminateProcess，pid 文件残留
	// 正好覆盖陈旧检测清理路径）
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	// 崩溃后应能重新获取（Unix 内核自动释放 / 非 Unix 陈旧检测清理）
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("崩溃后应能重新获取锁: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close 应无错: %v", err)
	}
}
