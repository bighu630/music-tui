package ui

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-tui/model"
	"music-tui/queue"
	"music-tui/session"
)

// writeTestWAV 写一个 1 秒 WAV（8000Hz 单声道 16bit PCM：byteRate=16000，
// data chunk 16000 字节）到 t.TempDir() 并返回绝对路径。
// wavDuration = dataLen/byteRate = 16000/16000 = 1.0。
// 字节序与 local 包 WAV 解析器一致（BE 读取 byteRate/data 大小，
// 参考 local/tag_test.go 的 writeWAV 与 local/tag.go 的 wavDuration）。
// 文件名 "歌手甲 - 曲目甲.wav"：刷新（FromPath）后 Artist=歌手甲 非空——
// 歌词 /api/get 守卫要求 artist 非空且 Duration≥1，e2e 测试才能断言
// duration 参数（空 artist 会退化为无 duration 的 /api/search）。
func writeTestWAV(t *testing.T) string {
	t.Helper()
	return writeTestWAVDur(t, 1.0)
}

// writeTestWAVDur 写一个 dur 秒的 WAV（8000Hz 单声道 16bit PCM：byteRate=16000，
// data chunk 16000*dur 字节）到 t.TempDir() 并返回绝对路径。
// wavDuration = dataLen/byteRate = (16000*dur)/16000 = dur。
// 参数化用于需要不同时长 fixture 的测试（如 Ended 恢复断言下一首真实时长）。
func writeTestWAVDur(t *testing.T, dur float64) string {
	t.Helper()
	dataLen := uint32(dur * 16000)
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	binary.BigEndian.PutUint32(hdr[4:8], 36+dataLen)
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.BigEndian.PutUint32(hdr[16:20], 16) // fmt chunk 大小
	binary.BigEndian.PutUint16(hdr[20:22], 1)  // PCM
	binary.BigEndian.PutUint16(hdr[22:24], 1)  // 单声道
	binary.BigEndian.PutUint32(hdr[24:28], 8000)   // sample rate
	binary.BigEndian.PutUint32(hdr[28:32], 16000)  // byteRate = 8000*1*2
	binary.BigEndian.PutUint16(hdr[32:34], 2)      // blockAlign
	binary.BigEndian.PutUint16(hdr[34:36], 16)     // bitsPerSample
	copy(hdr[36:40], "data")
	binary.BigEndian.PutUint32(hdr[40:44], dataLen) // data chunk 大小
	data := make([]byte, dataLen)
	p := filepath.Join(t.TempDir(), "歌手甲 - 曲目甲.wav")
	if err := os.WriteFile(p, append(hdr, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// localStaleTrack 构造带陈旧元数据的本地曲目：Duration=0 模拟时长解析功能
// 上线前扫描进播放列表/会话的 JSON 快照（真实文件仍在磁盘）。
func localStaleTrack(path string) model.Track {
	return model.Track{
		ID:       path,
		Title:    "旧标题",
		Artist:   "旧歌手",
		Duration: 0,
		URL:      path,
		Source:   model.SourceLocal,
	}
}

// TestResumeRefreshesLocalTrackDuration 会话恢复（NewModel 恢复分支）对本地
// 曲目重新读取元数据：快照 Duration=0 → 刷新为真实时长 1.0。
// 恢复走 PlayPaused 不经过 beginPlay，必须在此单独刷新。
func TestResumeRefreshesLocalTrackDuration(t *testing.T) {
	path := writeTestWAV(t)
	q := queue.New()
	q.Replace(localStaleTrack(path))
	st := session.State{Queue: q.Snapshot(), Position: 0}

	m, _ := newResumeTestModel(t, &st, nil)

	if m.state.Track == nil || m.state.Track.Duration != 1.0 {
		t.Fatalf("恢复后 state.Track 时长应刷新为 1.0, got %+v", m.state.Track)
	}
	if m.resume == nil || m.resume.track.Duration != 1.0 {
		t.Fatalf("恢复后 resume.track 时长应刷新为 1.0, got %+v", m.resume)
	}
}

// TestBeginPlayRefreshesLocalTrackDuration 手动播放（beginPlay）对本地曲目
// 重新读取元数据：快照 Duration=0 → 刷新为真实时长 1.0，后续 fetchLyricsCmd
// 自动携带正确时长。
func TestBeginPlayRefreshesLocalTrackDuration(t *testing.T) {
	path := writeTestWAV(t)
	m, _ := newResumeTestModel(t, nil, nil)

	m, _ = m.beginPlay(localStaleTrack(path))

	if m.state.Track == nil || m.state.Track.Duration != 1.0 {
		t.Fatalf("beginPlay 后 state.Track 时长应刷新为 1.0, got %+v", m.state.Track)
	}
}

// TestResumeEndedAdvancesToRefreshedNext 回归：Ended 恢复且有下一首时，元数据
// 刷新必须作用于 queue.Next() 之后的最终曲目（此前刷新先于 Ended 分支执行，
// 结果被下一首的陈旧快照覆盖——恢复的曲目仍 Duration=0：客户端守卫虽防住
// lrclib /api/get 400，但 search 兜底按 30s 阈值过滤会把真实歌曲全部超限，
// 静默无歌词）。两个 fixture 时长不同（1s/2s），断言精确到下一首真实时长。
func TestResumeEndedAdvancesToRefreshedNext(t *testing.T) {
	path1 := writeTestWAV(t)       // 1.0s
	path2 := writeTestWAVDur(t, 2) // 2.0s，与 path1 区分
	q := queue.New()
	q.Replace(localStaleTrack(path1))
	q.Add(localStaleTrack(path2))
	st := session.State{Queue: q.Snapshot(), Position: 180, Ended: true}

	m, _ := newResumeTestModel(t, &st, nil)

	if m.state.Track == nil || m.state.Track.ID != path2 {
		t.Fatalf("ended 恢复应跳到下一首（刷新后）: %+v", m.state.Track)
	}
	if m.state.Track.Duration != 2.0 {
		t.Fatalf("下一首快照 Duration=0 应在 Ended 分支后刷新为真实时长 2.0, got %v", m.state.Track.Duration)
	}
	if m.state.Position != 0 {
		t.Errorf("ended 恢复 Position 应为 0, got %v", m.state.Position)
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1（已推进到下一首）", m.queue.CurrentIndex())
	}
	if m.resume == nil || m.resume.track.Duration != 2.0 || m.resume.track.ID != path2 {
		t.Errorf("resume 信息应携带刷新后的下一首: %+v", m.resume)
	}
}

// TestBeginPlayKeepsKnownLocalDuration 对照：本地曲目 Duration 已正确（≥1）时
// 门控跳过文件重读——时长保持原值，不产生同步文件 I/O（每次切歌全量扫描
// 会阻塞 UI 消息循环）。
func TestBeginPlayKeepsKnownLocalDuration(t *testing.T) {
	path := writeTestWAV(t) // 磁盘真实 1.0s
	tr := localStaleTrack(path)
	tr.Duration = 2.0 // 快照时长“已知”，不应被门控重读覆盖

	m, _ := newResumeTestModel(t, nil, nil)
	m, _ = m.beginPlay(tr)

	if m.state.Track == nil || m.state.Track.Duration != 2.0 {
		t.Fatalf("Duration≥1 的本地曲目不应被重读, got %+v", m.state.Track)
	}
}

// TestBeginPlayKeepsRemoteDuration 对照：非本地曲目（youtube）不刷新，
// Duration 保持原值。
func TestBeginPlayKeepsRemoteDuration(t *testing.T) {
	m, _ := newResumeTestModel(t, nil, nil)
	tr := testTrack("remote")

	m, _ = m.beginPlay(tr)

	if m.state.Track == nil || m.state.Track.Duration != 200 {
		t.Fatalf("远程曲目不应刷新时长, got %+v", m.state.Track)
	}
}

// TestBeginPlayLyricsRequestCarriesRefreshedDuration 端到端：刷新后的真实
// 时长进入歌词 /api/get 请求参数（duration=1.00）——修复目标即防陈旧
// Duration=0 触发 lrclib 400 整链中断。
func TestBeginPlayLyricsRequestCarriesRefreshedDuration(t *testing.T) {
	path := writeTestWAV(t)
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	m, _ := newResumeTestModelWithLyricsHandler(t, nil, nil, srv.Config.Handler)

	m, cmd := m.beginPlay(localStaleTrack(path))
	// tea.Batch 只打包不执行：BatchMsg 须回灌 update（ui Update 的 BatchMsg 分支
	// 同步展开子命令，与真实 bubbletea 事件循环语义一致）后才真正发请求。
	for _, msg := range execCmds(cmd) {
		m, cmd = update(m, msg)
		_ = execCmds(cmd)
	}

	var found bool
	for _, q := range queries {
		if strings.Contains(q, "duration=1.00") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("歌词请求应携带刷新后的 duration=1.00, 全部 query = %v", queries)
	}
}
