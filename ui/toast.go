// toast 通知状态机：单条覆盖 + 定时自动消失。
// toast 渲染不参与布局计算（root.View 末尾对最后一行=状态栏行做左对齐整行
// 覆盖，报错期间临时显示、消失后恢复），因此出现/消失都不会引起排版跳动。
// 本文件为纯逻辑（无 bubbletea 依赖），定时器命令 showToast 在 root.go
// （Model 方法，需 tea.Tick）。

package ui

import "time"

// toastKind toast 类型：决定样式颜色与显示时长。
type toastKind int

const (
	toastError   toastKind = iota // 红 ⚠，5s
	toastWarning                  // 黄 ⚠，5s
	toastSuccess                  // 绿 ✔，3s
	toastInfo                     // 默认色 ℹ，3s
)

// toast 显示时长（包级变量：测试可调小以快进定时器，同 retryBackoff 模式）。
var (
	toastErrorDuration   = 5 * time.Second
	toastSuccessDuration = 3 * time.Second
)

// toast 单条通知（覆盖语义：新 toast 替换旧 toast 并重置定时器）。
type toast struct {
	text string
	kind toastKind
	id   uint64 // 单调递增；过期消息按 id 匹配，防止误清被新 toast 覆盖后的旧消息
}

// toastExpireMsg toast 消失定时器到期消息。
type toastExpireMsg struct {
	id uint64
}

// toastDuration 返回 kind 对应的显示时长：错误/警告 5s，成功/信息 3s。
func toastDuration(kind toastKind) time.Duration {
	switch kind {
	case toastError, toastWarning:
		return toastErrorDuration
	default:
		return toastSuccessDuration
	}
}

// expireToast 处理过期消息：id 与当前 toast 匹配才清除；不匹配
// （当前 toast 已被新消息覆盖）则保持现状。
func expireToast(cur *toast, id uint64) *toast {
	if cur != nil && cur.id == id {
		return nil
	}
	return cur
}
