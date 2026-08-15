# OpenAI 增强歌词匹配（feat/lyrics-ai）

## 已确认需求
1. OpenAI 可配置（api_key/model，缺省禁用）；纯 REST，temperature 0.2，JSON 输出 {is_song, title, artist}，prompt 含繁→简 + feat. 示例
2. 匹配流程：确定性匹配 → 未命中且配置 AI → AI 清洗 → 重查 lrclib（get 优先/search 评分）→ 结果入缓存；AI 失败降级确定性结果
3. 时长规则：AI 路径候选 duration 差距 ≤3s 才采用，差距最小者优先（确定性路径保持现有 30s 阈值不变）
4. 双缓存：AI 结果 JSONL（含 is_song=false 负缓存）+ 歌词 LRC 文件缓存（title-artist 命名）
5. UI：AI 来源歌词标注「AI 匹配」小标识；默认 model = gpt-4o-mini
6. 设计文档追加第 19 章；全量验证 build/vet/test(-race)

## TDD 任务
- [ ] lyrics/ai_test.go：parseAIResponse（围栏/裸 JSON/前后文本/非歌曲/缺 is_song/截断/垃圾/错误形状）
- [ ] lyrics/ai_test.go：Identify（httptest：请求体 model/temperature/prompt、鉴权头、内容提取、非 200、5xx 重试、401 不重试）
- [ ] lyrics/client_test.go：FetchForQuery 严格阈值（≤3s 采用差距最小、全部 >3s 拒绝、get 命中优先）
- [ ] lyrics/aicache_test.go：JSONL 读写/负缓存/损坏行跳过重写/并发 append
- [ ] lyrics/lrccache_test.go：synced/plain 存取、sanitize、未知名 miss
- [ ] lyrics/enhanced_test.go：全流程（确定性命中不调 AI；无 AI 配置降级；AI→重查命中；负缓存；AI 失败降级；>3s 弃用；歌词缓存命中免请求；AI 结果缓存命中免调用）
- [ ] config/config_test.go：openai 缺省禁用 / model 缺省默认 / 显式空 api_key 禁用
- [ ] ui/home_test.go：AI 来源歌词渲染「AI 匹配」标识
- [ ] 现有测试不回归（client/config/ui/main）

## 实现
- [ ] lyrics/ai.go：OpenAIClient + AIResult + parseAIResponse + prompt
- [ ] lyrics/client.go：fetchOne 加 maxDelta 参数 + chooseBestWithin + FetchForQuery
- [ ] lyrics/aicache.go + lrccache.go
- [ ] lyrics/enhanced.go：EnhancedClient + Fetcher 接口
- [ ] config：OpenAI 配置节
- [ ] ui/root.go：Fetcher 接口接线；ui/home.go：AI 标识
- [ ] main.go：配置 AI 时组装 EnhancedClient
- [ ] 设计文档第 19 章

## 验证
- [ ] go build ./... && go vet ./... && go test ./...（含 -race）
- [ ] 根目录 TODO.md 更新

## OpenAI 增强歌词匹配（用户已确认方案）

- [x] 方案确认：OpenAI 可配置（缺省禁用）+ AI 清洗→重查 lrclib + ≤3s 严格时长规则 + 双缓存 + UI「AI 匹配」标识
- [x] 设计文档追加第 19 章（配置/流程/时长规则/缓存/测试策略/已知限制）
- [x] 实现：lyrics/ai.go（REST 客户端 + prompt + 解析）、client.go（FetchForQuery + 3s 严格阈值）、aicache.go（JSONL 负缓存）、lrccache.go（LRC 文件缓存）、enhanced.go（混合流程编排）、config OpenAI 节、ui Fetcher 接口 + 「AI 匹配」标识、main 接线
- [x] 测试：ai 11 + 严格阈值 5 + aicache 7 + lrccache 8 + enhanced 10 + config 6 + ui 1 = 48 个新测试，全绿（含 -race）
- [ ] 真机验收（待用户）：配置 api_key 后播放噪声标题曲目 → 歌词经 AI 兜底命中且显示「AI 匹配」；二次播放零请求（缓存命中）；无配置行为不变
