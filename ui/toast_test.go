package ui

import (
	"testing"
	"time"
)

// TestToastDuration kind→时长映射：错误/警告 5s，成功/信息 3s。
func TestToastDuration(t *testing.T) {
	toastErrorDuration = 5 * time.Second
	toastSuccessDuration = 3 * time.Second
	cases := []struct {
		kind toastKind
		want time.Duration
	}{
		{toastError, 5 * time.Second},
		{toastWarning, 5 * time.Second},
		{toastSuccess, 3 * time.Second},
		{toastInfo, 3 * time.Second},
	}
	for _, tc := range cases {
		if got := toastDuration(tc.kind); got != tc.want {
			t.Errorf("toastDuration(%v) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestExpireToastIDMatch 过期消息 id 与当前 toast 匹配 → 清除。
func TestExpireToastIDMatch(t *testing.T) {
	cur := &toast{text: "播放失败", kind: toastError, id: 1}
	if got := expireToast(cur, 1); got != nil {
		t.Errorf("expireToast 匹配 id 应清除, got %+v", got)
	}
}

// TestExpireToastIDMismatch 过期消息 id 不匹配（当前 toast 已被新消息覆盖）→ 保留。
func TestExpireToastIDMismatch(t *testing.T) {
	cur := &toast{text: "新消息", kind: toastWarning, id: 2}
	if got := expireToast(cur, 1); got != cur {
		t.Errorf("expireToast 不匹配 id 应保留当前 toast, got %+v", got)
	}
}

// TestExpireToastNil 无活跃 toast 时过期消息安全丢弃。
func TestExpireToastNil(t *testing.T) {
	if got := expireToast(nil, 1); got != nil {
		t.Errorf("无 toast 时过期消息应丢弃, got %+v", got)
	}
}
