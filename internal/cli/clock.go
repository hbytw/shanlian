package cli

import (
	"fmt"
	"time"

	"slx/internal/buildinfo"
)

// clockCheck 粗略判断系统时钟是否可信。
//
// 为什么在意：隧道握手会带时间戳以防重放，集群里各节点时间差太大会导致
// 握手被拒。而 OpenWrt/嵌入式设备断电后时钟归零是常见故障。
//
// 这里不依赖网络（doctor 可能在没有连通性时运行），只用两个本地信号：
//   - 系统时间是否早于构建时间（说明时钟明显错乱）
//   - 系统时间是否处于明显不合理的年份
func clockCheck() Check {
	now := time.Now()

	if now.Year() < 2024 {
		return Check{
			Name: "系统时钟", Layer: "转发", Status: StatusFail,
			Detail: "系统时间异常: " + now.Format(time.RFC3339),
			Hint:   "设备可能断电后时钟归零 —— 启用 NTP（OpenWrt: /etc/config/system 里 option ntp）或手动 date -s 设置",
		}
	}

	if bt, err := time.Parse(time.RFC3339, buildinfo.BuildTime); err == nil {
		if now.Add(24 * time.Hour).Before(bt) {
			return Check{
				Name: "系统时钟", Layer: "转发", Status: StatusFail,
				Detail: fmt.Sprintf("系统时间 %s 早于程序构建时间 %s",
					now.Format(time.RFC3339), bt.Format(time.RFC3339)),
				Hint: "时钟错乱会导致握手时间戳校验失败 —— 先校准时间，建议启用 NTP",
			}
		}
		skew := now.Sub(bt)
		if skew > 5*365*24*time.Hour {
			return Check{
				Name: "系统时钟", Layer: "转发", Status: StatusWarn,
				Detail: fmt.Sprintf("距构建时间已 %d 天，时钟可能偏快", int(skew.Hours()/24)),
				Hint:   "建议确认 NTP 是否正常",
			}
		}
	}

	return Check{Name: "系统时钟", Layer: "转发", Status: StatusPass, Detail: now.Format(time.RFC3339)}
}
