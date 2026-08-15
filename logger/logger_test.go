package logger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setup 把日志重定向到临时文件并重新 Init；t.Cleanup 恢复默认状态。
func setup(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	logPath = path
	Init(LevelDebug)
	t.Cleanup(func() {
		mu.Lock()
		if file != nil {
			file.Close()
			file = nil
		}
		mu.Unlock()
		logPath = filepath.Join(os.TempDir(), "music-tui.log")
		MaxFileSize = 5 * 1024 * 1024
	})
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"INFO", LevelInfo}, // 大小写敏感：非法回落
		{"verbose", LevelInfo},
	}
	for _, c := range cases {
		if got := ParseLevel(c.in); got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeLevel(t *testing.T) {
	if got := NormalizeLevel("warn"); got != "warn" {
		t.Errorf("NormalizeLevel(warn) = %q", got)
	}
	if got := NormalizeLevel(""); got != "info" {
		t.Errorf("NormalizeLevel('') = %q, want info", got)
	}
	if got := NormalizeLevel("bogus"); got != "info" {
		t.Errorf("NormalizeLevel(bogus) = %q, want info", got)
	}
}

func TestLevelFiltering(t *testing.T) {
	path := setup(t)
	SetLevel(LevelInfo)
	Debug("debug line")
	Info("info line")
	Warn("warn line")
	Error("error line")
	content := readFile(t, path)
	if strings.Contains(content, "debug line") {
		t.Error("debug 行不应写入（当前级别 info）")
	}
	for _, want := range []string{"info line", "warn line", "error line"} {
		if !strings.Contains(content, want) {
			t.Errorf("缺少 %q", want)
		}
	}
}

func TestLineFormat(t *testing.T) {
	path := setup(t)
	Info("hello %s", "world")
	content := readFile(t, path)
	if !strings.Contains(content, "[INFO] hello world") {
		t.Errorf("行格式错误: %q", content)
	}
	// 时间戳前缀形如 2006-01-02 15:04:05.000（索引 4/10/23 为分隔符）
	if len(content) < 24 || content[4] != '-' || content[10] != ' ' || content[23] != ' ' {
		t.Errorf("时间戳前缀错误: %q", content)
	}
}

func TestInitFailureDegrades(t *testing.T) {
	// 日志路径指向不存在目录 → Init 失败 → 写不 panic、不创建文件
	logPath = filepath.Join(t.TempDir(), "no-such-dir", "x.log")
	Init(LevelDebug)
	Info("should not panic")
	SetLevel(LevelInfo)
}

func TestConcurrentWrites(t *testing.T) {
	path := setup(t)
	const goroutines = 8
	const perG = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				Info("g%d line %d", g, i)
			}
		}(g)
	}
	wg.Wait()
	content := readFile(t, path)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("行数 = %d, want %d（并发写丢行）", len(lines), goroutines*perG)
	}
	for _, ln := range lines {
		if !strings.Contains(ln, "[INFO] g") {
			t.Fatalf("行格式异常: %q", ln)
		}
	}
}
