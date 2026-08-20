package ui

import (
	"errors"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
)

// ---- 全局 spinner tick 链按需续发（P0-2） ----

// 空闲（无 spinner、无加载）时 tick 不续发：播放中 10fps 空转渲染停止。
func TestSpinnerTickStopsWhenIdle(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, cmd := update(m, spinner.TickMsg{Time: time.Now()})
	if cmd != nil {
		t.Fatalf("空闲时 tick 不应续发, cmd=%v", cmd)
	}
}

// 播放开始（歌词加载中 + 加载计时）→ tick 链存活。
func TestSpinnerTickKeepsWhilePlayingLoad(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, cmd = update(m, spinner.TickMsg{Time: time.Now()})
	if cmd == nil {
		t.Fatal("歌词加载中 tick 应续发（spinner 动画）")
	}
}

// TrackStarted 后歌词已就绪 → 链停止；且加载中提示须显式复位（tick 链已
// 停止不再派生，否则播放中进度行永久显示"加载中…"）。
func TestSpinnerTickStopsAndLoadingClearedAfterTrackReady(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// 前提：2s 未就绪 → 加载中提示出现（tick 派生）
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(3 * time.Second)})
	if !m.home.loading {
		t.Fatal("前提：2s 未就绪应显示加载中")
	}
	// 歌词就绪 + TrackStarted（加载结束）
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]x\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.home.loading {
		t.Fatal("TrackStarted 后加载中提示应清除（显式复位，不依赖 tick）")
	}
	// 无 spinner 无加载 → 链停止
	m, cmd = update(m, spinner.TickMsg{Time: time.Now()})
	if cmd != nil {
		t.Fatalf("播放就绪后 tick 不应续发, cmd=%v", cmd)
	}
}

// TrackEnded/ErrorEvent 同样须清除加载中提示（tick 链已停不再派生，漏清除会
// 永久显示"加载中…"）。
func TestLoadingClearedOnTrackEndedAndError(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// 前提：2s 未就绪 → 加载中提示出现（tick 派生）
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(3 * time.Second)})
	if !m.home.loading {
		t.Fatal("前提：2s 未就绪应显示加载中")
	}
	// 歌词已就绪（无 spinner 继续驱动派生）：TrackEnded 须显式清除
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]x\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.home.loading {
		t.Fatal("TrackEnded 后加载中提示应清除")
	}
	// ErrorEvent（非取流失败：不触发自动重试）同样清除
	m, cmd = m.startPlay(testTrack("t2"))
	_ = execCmds(cmd)
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(3 * time.Second)})
	if !m.home.loading {
		t.Fatal("前提：t2 加载中提示应显示")
	}
	m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: errors.New("连接断开")}})
	if m.home.loading {
		t.Fatal("ErrorEvent 后加载中提示应清除")
	}
}

// TrackStarted 已到但歌词仍在加载：spinner 仍需动画，链保持。
func TestSpinnerTickKeepsWhenLyricsStillLoading(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// TrackStarted 到达但歌词结果未回（仍 lyricsLoading）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd = update(m, spinner.TickMsg{Time: time.Now()})
	if cmd == nil {
		t.Fatal("歌词加载中 tick 应续发（即使 TrackStarted 已到）")
	}
}

// 加载起点重复触发（100ms 窗口内多次 beginPlay 等）不得并发出多条 tick 链：
// armSpinnerTick 只在链死亡时启动一次。
func TestArmSpinnerTickStartsOnce(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if !m.tickLive {
		t.Fatal("beginPlay 应启动 tick 链（tickLive=true）")
	}
	// 链存活：重复 arm 不再启动（防双链）
	m2, cmd2 := m.armSpinnerTick()
	if cmd2 != nil {
		t.Fatal("链已存活时重复 arm 不应再启动（双链 → 渲染翻倍）")
	}
	// 链停止（TrackStarted + 歌词就绪 + tick 不续发）
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]x\n"))
	m2, _ = update(m2, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m2, _ = update(m2, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m2, cmd2 = update(m2, spinner.TickMsg{Time: time.Now()})
	if cmd2 != nil {
		t.Fatal("就绪后 tick 应停止")
	}
	if m2.tickLive {
		t.Fatal("链停止后 tickLive 应为 false")
	}
	// 新一轮播放：重新启动
	m3, cmd3 := m2.startPlay(testTrack("t2"))
	_ = execCmds(cmd3)
	if !m3.tickLive {
		t.Fatal("新一轮加载应重新启动链")
	}
}

// 搜索 Enter → 链启动（spinner 动画）；结果到达 → 链停止。
func TestSpinnerTickSearchFlow(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}, nil)
	m, _ = update(m, tea.KeyPressMsg{Code: '4', Text: "4"}) // 数字键直达搜索页
	m, _ = update(m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.searchPage.state != searchLoading {
		t.Fatalf("state = %v, want searchLoading", m.searchPage.state)
	}
	// Enter 委托按需启动 tick 链：展开 batch 后 TickMsg 到达时搜索仍在加载
	//（结果尚未回灌）→ 链应存活
	msgs := execSearchCmds(cmd)
	var tick tea.Msg
	var res searchResultsMsg
	for _, msg := range msgs {
		switch mm := msg.(type) {
		case spinner.TickMsg:
			tick = mm
		case searchResultsMsg:
			res = mm
		}
	}
	if tick == nil || res.tracks == nil {
		t.Fatalf("batch 应产生 TickMsg 与 searchResultsMsg, got %#v", msgs)
	}
	m, cmd = update(m, tick)
	if cmd == nil {
		t.Fatal("搜索加载中 tick 应续发")
	}
	// 结果到达：searchDone → 链停止
	m, _ = update(m, res)
	m, cmd = update(m, spinner.TickMsg{Time: time.Now()})
	if cmd != nil {
		t.Fatal("搜索完成后 tick 应停止")
	}
}
