// Package search 定义搜索适配器接口与 YouTube 实现。
package search

import (
	"context"

	"music-tui/model"
)

// SearchAdapter 是音乐搜索适配器接口；未来新增来源（如网易云）
// 只需再实现一个适配器。
type SearchAdapter interface {
	// Search 按关键词搜索，返回歌曲列表；ctx 用于超时与取消。
	Search(ctx context.Context, query string) ([]model.Track, error)
}
