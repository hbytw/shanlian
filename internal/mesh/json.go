package mesh

import (
	"encoding/json"
	"fmt"
)

// maxCtrlBody 是单个控制消息体的上限。
//
// 对端发来的内容长度不可信，解析前先设上限，避免恶意或异常节点
// 用超大 JSON 消耗内存（低端路由器只有几十 MB 可用）。
const maxCtrlBody = 8 << 10

// jsonUnmarshal 是带长度保护的 JSON 解析入口。
func jsonUnmarshal(b []byte, v any) error {
	if len(b) > maxCtrlBody {
		return fmt.Errorf("控制消息体过大: %d 字节（上限 %d）", len(b), maxCtrlBody)
	}
	return json.Unmarshal(b, v)
}
