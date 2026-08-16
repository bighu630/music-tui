package singleinstance

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// processAlive 判断 pid 是否存活；pid 文件方案用于陈旧锁检测。
// 包变量便于测试注入。
var processAlive = defaultProcessAlive

// acquirePidLock 用 pid 文件方案获取锁（非 Unix 平台；逻辑不依赖平台
// API，可在 Unix 上单测）。O_EXCL 创建锁文件并写入本进程 pid；文件已
// 存在则读其中 pid，进程已死/内容损坏视为陈旧锁，删除后重试一次。
func acquirePidLock(path string) (*Lock, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if _, werr := fmt.Fprintf(f, "%d", os.Getpid()); werr != nil {
				f.Close()
				os.Remove(path)
				return nil, fmt.Errorf("写入锁文件失败: %w", werr)
			}
			return &Lock{f: f, path: path, pid: os.Getpid()}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("创建锁文件失败: %w", err)
		}
		lastErr = err

		// 文件已存在：读其中 pid 判断是否为陈旧锁。
		pid, ok := readLockPid(path)
		if !ok {
			// 空/无法解析的内容：可能是并发创建者正处于 O_EXCL 创建与
			// 写入之间的窗口——立即删除会删掉对方刚建的文件，对方随后
			// 重试 O_EXCL 成功，造成双实例。稍候重读一次，仍无法读取
			// 才按陈旧处理。
			time.Sleep(50 * time.Millisecond)
			pid, ok = readLockPid(path)
		}
		if ok && pid == os.Getpid() {
			// 残留锁含本进程 pid（pid 重用）：并发实例不可能与本进程
			// 同 pid，直接按陈旧处理。
			ok = false
		}
		if ok && processAlive(pid) {
			return nil, fmt.Errorf("%w（锁文件: %s）", ErrInstanceRunning, path)
		}

		// 陈旧锁：内容损坏 / pid 无效 / 进程已死，或重读时文件已被并发
		// 删除。删除前确认路径是普通文件——锁路径误配为目录时绝不能
		// os.Remove 删掉目录（空目录会被 Remove 成功删除）。
		st, lerr := os.Lstat(path)
		if lerr != nil {
			continue // 文件已不存在（被并发删除）：直接重试 O_EXCL
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("锁文件路径不是普通文件: %s", path)
		}
		os.Remove(path) // Remove 失败可能因并发删除，忽略
		continue
	}
	return nil, fmt.Errorf("清理陈旧锁文件后仍无法获取锁: %w", lastErr)
}

// readLockPid 读取锁文件中的 pid。文件不存在、读取失败、内容不是
// 正整数（含空内容）都返回 ok=false。
func readLockPid(path string) (pid int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// releasePidLock 删除本实例持有的 pid 锁文件：删除前校验文件内容仍
// 是本实例 pid，防止 pid 重用后误删他人锁；崩溃残留由下次启动的
// 陈旧检测清理。pid 方案共享逻辑——非 unix 平台的 Acquire 与 unix
// 上的 pid 锁（仅测试路径产生）都经由 Close 走到这里。
func releasePidLock(l *Lock) {
	if pidFileOwnedBy(l.path, l.pid) {
		os.Remove(l.path)
	}
}

// pidFileOwnedBy 判断锁文件内容是否为本实例 pid（供 Close 时校验，
// 防止 pid 重用后误删他人锁文件）。
func pidFileOwnedBy(path string, pid int) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err == nil && got == pid
}
