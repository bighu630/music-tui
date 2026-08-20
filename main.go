// music-tui 是一个基于 YouTube 的终端音乐播放器。
// 配置加载 → 依赖检测 → 启动 mpv → 组装服务 → 启动 TUI → 退出清理。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/cache"
	"music-tui/config"
	"music-tui/cover"
	"music-tui/coverrender"
	"music-tui/history"
	"music-tui/logger"
	"music-tui/lyrics"
	"music-tui/lyricshm"
	"music-tui/mpris"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/search"
	"music-tui/session"
	"music-tui/singleinstance"
	"music-tui/ui"
	"music-tui/ytm"
)

// version 是应用版本号，展示于 User-Agent 与错误信息中。
const version = "0.1.0"

// userAgent 遵循 lrclib 要求的 "应用名 版本 (主页)" 格式。
const userAgent = "music-tui " + version + " (https://github.com/example/music-tui)"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "music-tui:", err)
		os.Exit(1)
	}
}

func run() error {
	// 0. 日志初始化：先默认 info（config 损坏/未加载也有日志），
	// config 加载成功后调整级别
	logger.Init(logger.LevelInfo)

	// 1. 配置加载：依赖检测需用配置的 ytdlp.path，故提前到依赖检测之前。
	// 首次运行会在此生成默认配置文件（原子写+rename，无并发问题）。
	cfgRoot, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("获取用户配置目录失败: %w", err)
	}
	cfg, err := loadConfig(filepath.Join(cfgRoot, "music-tui", "config.json"))
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	logger.SetLevel(logger.ParseLevel(cfg.Log.Level))

	// 2. 运行时依赖检测（缺失即报错退出，附平台安装命令）；yt-dlp 优先用
	// 配置的 ytdlp.path（配置了则只检测该路径，失败不回落 PATH）
	mpvPath, err := requireTool("mpv")
	if err != nil {
		return err
	}
	ytdlpPath, err := requireYtdlp(cfg.Ytdlp.Path)
	if err != nil {
		return err
	}

	// 2.5 单实例检测：已有实例在运行则报错退出（Unix: flock，内核自动释放；
	// 非 Unix: pid 文件 + 陈旧检测）。锁持有至 run 返回。
	lock, err := singleinstance.Acquire(filepath.Join(os.TempDir(), "music-tui.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()

	// 3. 数据目录准备（config 已在步骤 1 加载）
	hist, err := loadHistory(filepath.Join(cfgRoot, "music-tui", "history.json"))
	if err != nil {
		return fmt.Errorf("加载历史记录失败: %w", err)
	}
	sess, err := loadSession(filepath.Join(cfgRoot, "music-tui", "session.json"))
	if err != nil {
		return fmt.Errorf("加载会话失败: %w", err)
	}
	pls, err := loadPlaylists(filepath.Join(cfgRoot, "music-tui", "playlists.json"))
	if err != nil {
		return fmt.Errorf("加载播放列表失败: %w", err)
	}
	ytStore, err := loadYTM(filepath.Join(cfgRoot, "music-tui", "ytm.json"))
	if err != nil {
		return fmt.Errorf("加载 YT Music 配置失败: %w", err)
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("获取用户缓存目录失败: %w", err)
	}
	covers, err := cover.NewFetcher(filepath.Join(cacheRoot, "music-tui", "covers"))
	if err != nil {
		return fmt.Errorf("初始化封面缓存失败: %w", err)
	}
	// yt-dlp 全局 cookie：复用 YT Music 登录配置（未登录则无 cookie）。
	// 启动时快照：浏览器 cookie 过期需重启应用刷新（与 mpv 取流参数限制一致）。
	cookieFile, _ := ytStore.CookieFile() // 未登录/不可读 → 无 cookie，不阻止启动
	ytdlpHeaders := cfg.Ytdlp.Headers
	cm := loadCache(cfg.Cache, ytdlpPath, cfg.Ytdlp.Proxy, cookieFile, ytdlpHeaders)

	// 4. 启动 mpv（defer 保证退出时清理进程与 socket）
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("music-tui-%d.sock", os.Getpid()))
	mpv := player.NewMpvPlayer(mpvPath, sockPath, cookieFile, ytdlpHeaders)
	// 取流附加配置（自定义 yt-dlp 路径/代理）必须在 Start 之前设置。
	// 传 cfg.Ytdlp.Path 原始配置值而非 requireYtdlp 解析后的路径：
	// 未配置（空）时保持 mpv 启动参数与现状逐字节一致（不附加 script-opts）；
	// 自定义时该值已被 requireYtdlp 校验存在且可执行。
	mpv.SetYtdlpOptions(cfg.Ytdlp.Path, cfg.Ytdlp.Proxy)
	if err := mpv.Start(); err != nil {
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}
	defer mpv.Close()

	// 4.5 MPRIS 服务（仅 Linux 有效；非 Linux 为 no-op 桩）。
	// 连接/注册失败仅警告，绝不影响播放器主功能。
	mprisSrv := mpris.NewServer(mpv)
	// 封面缓存目录注入：metadataFor 命中缓存时 artUrl 用 file:// 本地路径
	//（本地歌曲 CoverURL 恒空、YouTube 原始 URL 常 404）；与 cover.NewFetcher
	// 同一路径表达式。
	mprisSrv.SetCoverCacheDir(filepath.Join(cacheRoot, "music-tui", "covers"))
	if err := mprisSrv.Start(); err != nil {
		logger.Warn("MPRIS 服务不可用（不影响播放器）: %v", err)
	} else {
		defer mprisSrv.Close()
	}

	// 5. 组装服务并启动 TUI（退出后 run 返回，defer 清理 mpv）
	searchAdapter := search.NewYouTubeAdapter(ytdlpPath)
	searchAdapter.SetGlobalYTDlp(cookieFile, ytdlpHeaders)
	searchAdapter.SetGlobalProxy(cfg.Ytdlp.Proxy)
	lc := lyrics.NewClient(userAgent)
	var lyClient lyrics.Fetcher = lc
	if cfg.OpenAI.APIKey != "" {
		// OpenAI 增强歌词：AI 清洗标题后重查 lrclib（含 AI 结果/歌词双缓存）。
		// 缓存初始化失败仅警告并降级为确定性匹配——增强功能不影响主功能。
		ai := lyrics.NewOpenAIClientWithBaseURL(cfg.OpenAI.APIKey, cfg.OpenAI.Model, cfg.OpenAI.BaseURL)
		enhanced, err := lyrics.NewEnhancedClient(lc, ai, filepath.Join(cfg.Cache.Dir, "lyrics"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "music-tui: 警告：AI 歌词缓存初始化失败，已降级确定性匹配: %v\n", err)
			logger.Warn("AI 歌词缓存初始化失败，已降级确定性匹配: %v", err)
		} else {
			// 中文歌词源链（用户确认顺序：网易云 → QQ → lrclib）：
			// 匿名接口无需登录，优先于 lrclib 严格重查查询。
			enhanced.EnableCNSources(lyrics.NewNeteaseClient(), lyrics.NewQQMusicClient())
			lyClient = enhanced
		}
	}
	// 歌词文件写入器：lyric_file.enabled=false 时不创建（传 nil，ui 层 no-op）；
	// 非 Linux 平台 lyricshm.New 内部自动禁用（仅 Linux 生效）。
	var lyricFile *lyricshm.Writer
	if cfg.LyricFile.Enabled {
		lyricFile = lyricshm.New(lyricshm.DefaultPath)
	}
	model := ui.NewModel(
		mpv,
		searchAdapter,
		lyClient,
		covers,
		hist,
		sess,
		pls,
		cm,
		ytm.NewClient(ytStore, searchAdapter),
		mprisSrv.SetTrack,
		mprisSrv.RefreshMetadata, // 封面下载完成 → 重发带 file:// 缓存路径的 Metadata
		cookieFile != "" || len(ytdlpHeaders) > 0,
		lyricFile,
	)
	// MPRIS 队列控制注入：ui 侧桥实现 mpris 包的 controller 接口（编译期检查）；
	// 模式变更经 sink 同步回 MPRIS 属性（LoopStatus/Shuffle 投影 + PropertiesChanged）。
	mprisSrv.SetController(model.MprisController())
	model.MprisController().SetModeSink(mprisSrv.SyncMode)
	// CellMotion：点击/滚轮/拖拽（按下移动）必报，但无按键的悬停移动不再上报
	// ——Tab 悬停高亮随之下线（AllMotion 下鼠标任何移动都产生 MouseMsg → 全量
	// View 渲染，CPU 热点 3；悬停高亮与 CPU 目标不可兼得，取舍以 CPU 为准）。
	// 终端图形能力查询（封面渲染用）：必须在 TUI 接管 stdin 之前完成——运行期
	// 查询会与输入循环抢读。DA1（sixel）/ kitty 图形查询超时 250ms，无应答忽略。
	if mode, ok := coverrender.QueryCapability(250 * time.Millisecond); ok {
		coverrender.SetCapability(mode)
		logger.Info("终端能力查询: %v（应答 %q）", mode, coverrender.LastQueryRaw())
	} else if r := coverrender.LastQueryRaw(); r != "" {
		logger.Info("终端能力查询: 未确认（应答 %q）", r)
	} else {
		logger.Info("终端能力查询: 无应答")
	}
	// tmux 内：tmux 3.5+ 会原样中继 pane 的 kitty APC 给外层终端。查外层终端名
	// （tmux client_termname），确认是 kitty（含 xterm-kitty）才允许在 tmux 内用
	// kitty——避免 foot 等外层收到 kitty APC 变乱码。
	if os.Getenv("TMUX") != "" {
		if termName, ok := tmuxClientTermName(); ok && strings.Contains(strings.ToLower(termName), "kitty") {
			coverrender.SetCapability(coverrender.ModeKitty)
			coverrender.SetTMUXKittyRelay(true)
			logger.Info("tmux 外层终端 %q 支持 kitty，允许中继", termName)
		}
	}
	// 六边形 cell 高度自校准：画已知像素高测试图 + DSR 实测占用行数反推真实
	// cell 像素（ioctl 物理像素在 wayland 缩放下偏大；CSI 16t 部分终端不支持）。
	if w, h, rows, ok := coverrender.CalibrateCellSize(250 * time.Millisecond); ok {
		logger.Info("cell 自校准: %dx%d（测试图 %dpx 占用 %d 行）", w, h, 96, rows)
	}
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI 运行失败: %w", err)
	}
	return nil
}

// loadHistory 加载历史记录；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动。备份失败或重建失败则返回错误。
func loadHistory(path string) (*history.Store, error) {
	store, err := history.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：历史文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("历史文件损坏，已备份至 %s 并重建", backup)
	store, retryErr := history.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// loadPlaylists 加载播放列表文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动（与 loadHistory 同款降级）。
func loadPlaylists(path string) (*playlists.Store, error) {
	store, err := playlists.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：播放列表文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("播放列表文件损坏，已备份至 %s 并重建", backup)
	store, retryErr := playlists.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// loadConfig 加载配置文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免配置文件阻止应用启动（与 loadHistory 同款降级）。
func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：配置文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("配置文件损坏，已备份至 %s 并重建", backup)
	cfg, retryErr := config.Load(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return cfg, nil
}

// loadCache 初始化音频缓存：索引文件损坏（崩溃/断电截断）时备份后重试一次；
// 仍失败仅警告并降级为禁用态缓存——缓存绝不影响播放主功能（不阻止启动）。
func loadCache(opts cache.Options, ytdlpPath, proxy, cookieFile string, headers map[string]string) *cache.Manager {
	cm, err := cache.New(opts, ytdlpPath, proxy, cookieFile, headers)
	if err == nil {
		return cm
	}
	// 索引文件损坏（cache 包 IndexFile）→ 备份后重试
	if opts.Dir != "" {
		idxPath := filepath.Join(opts.Dir, cache.IndexFile)
		if _, serr := os.Stat(idxPath); serr == nil {
			backup := fmt.Sprintf("%s.corrupt-%d", idxPath, time.Now().UnixNano())
			if berr := os.Rename(idxPath, backup); berr == nil {
				fmt.Fprintf(os.Stderr, "music-tui: 警告：缓存索引损坏，已备份至 %s 并重建\n", backup)
				logger.Warn("缓存索引损坏，已备份至 %s 并重建", backup)
				if cm, retryErr := cache.New(opts, ytdlpPath, proxy, cookieFile, headers); retryErr == nil {
					return cm
				}
			}
		}
	}
	logger.Warn("缓存初始化失败（已降级为禁用）: %v", err)
	return cache.Disabled()
}

// loadSession 加载会话文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动（与 loadHistory 同款降级）。
func loadSession(path string) (*session.Store, error) {
	store, err := session.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：会话文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("会话文件损坏，已备份至 %s 并重建", backup)
	store, retryErr := session.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// loadYTM 加载 ytm 配置文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动（与 loadPlaylists 同款降级）。
func loadYTM(path string) (*ytm.Store, error) {
	store, err := ytm.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：YT Music 配置文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("YT Music 配置文件损坏，已备份至 %s 并重建", backup)
	store, retryErr := ytm.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// requireYtdlp 解析 yt-dlp 可执行文件路径：配置了 ytdlp.path 时只检测该
// 路径是否存在且可执行（失败即报错，不回落 PATH 查找）；未配置时与
// requireTool 一致，走 PATH 查找 "yt-dlp"。
func requireYtdlp(cfgPath string) (string, error) {
	if cfgPath == "" {
		path, err := exec.LookPath("yt-dlp")
		if err != nil {
			return "", fmt.Errorf("缺少依赖 yt-dlp，请先安装：\n%s", installHint("yt-dlp"))
		}
		return path, nil
	}
	path, err := exec.LookPath(cfgPath)
	if err != nil {
		return "", fmt.Errorf("yt-dlp 路径无效（config.json 的 ytdlp.path = %q）：\n请检查该配置项，或移除它以回落 PATH 查找：\n%s", cfgPath, installHint("yt-dlp"))
	}
	return path, nil
}

// requireTool 在 PATH 中查找依赖；缺失时返回带平台安装命令的错误。
func requireTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("缺少依赖 %s，请先安装：\n%s", name, installHint(name))
	}
	return path, nil
}

// installHint 返回当前平台安装 mpv/yt-dlp 的命令提示。
func installHint(tool string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install " + tool
	case "windows":
		if tool == "mpv" {
			return "winget install mpv"
		}
		return "pip install yt-dlp"
	default: // linux 等
		return "sudo apt install " + tool + "（dnf: sudo dnf install " + tool + "；pacman: sudo pacman -S " + tool + "）"
	}
}

// tmuxClientTermName 查询 tmux 外层终端名（client_termname，如 xterm-kitty /
// xterm-256color）。不在 tmux 内或查询失败返回 ok=false。
func tmuxClientTermName() (name string, ok bool) {
	if os.Getenv("TMUX") == "" {
		return "", false
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_termname}").Output()
	if err != nil || len(out) == 0 {
		return "", false
	}
	return string(trimSpaceBytes(out)), true
}

// trimSpaceBytes 简易去空白（避免单独引 bytes 包）。
func trimSpaceBytes(b []byte) []byte {
	s := 0
	for s < len(b) && (b[s] == ' ' || b[s] == '\t' || b[s] == '\n' || b[s] == '\r') {
		s++
	}
	e := len(b)
	for e > s && (b[e-1] == ' ' || b[e-1] == '\t' || b[e-1] == '\n' || b[e-1] == '\r') {
		e--
	}
	return b[s:e]
}
