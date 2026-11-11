package cli

import (
	"fmt"
	"log/slog"
	"os"

	"slx/internal/cnlist"
	"slx/internal/ipgeo"
	"slx/internal/logx"
)

// loadCNList 取国内网段：新鲜缓存 → 在线源 → 过期缓存；
// 全都拿不到时，用离线兜底表（ip2region 导出的静态列表）。
//
// 为什么要有兜底：原逻辑是「取不到就不启用分流」—— cnlist.Load 的原话是
// 「分不清境内境外比不分流危险得多」。这个判断本身没错，但它意味着
// 「在线源挂 + 本机没有缓存」= 代理整个不可用。
//
// 兜底表是随包部署的静态文件，不依赖网络，因此永远可用。
// 代价说清楚：它比在线源旧，而且不含港澳台（那部分通常该走隧道）。
// 用兜底时会打一条显眼的告警，并把来源标成「离线兜底」，
// 让 status / 日志里看得出当前用的是哪一份。
func loadCNList(sources []cnlist.Source, log *slog.Logger) (*cnlist.Result, error) {
	res, err := cnlist.Load("", sources, true)
	if err == nil {
		return res, nil
	}
	offline, oerr := offlineCNList()
	if oerr != nil {
		// 兜底也没有：报原来的错误，信息更全（在线源为什么失败）
		return nil, err
	}
	if log != nil {
		log.Warn("国内网段改用离线兜底表",
			logx.Event("dispatch.cnlist_offline"),
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, err.Error(),
			"entries", len(offline.CIDRs),
			logx.Hint("在线源不可用且本机没有可用缓存，已改用 ip2region 离线表；"+
				"它比在线源旧且不含港澳台，联网后会自动恢复"))
	}
	return offline, nil
}

// offlineCNList 从 ip2region 的 xdb 生成一份国内网段表。
func offlineCNList() (*cnlist.Result, error) {
	path := ipgeo.ResolvePath("")
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("没有离线兜底库 %s: %w", path, err)
	}
	exp, err := ipgeo.ExportCNC(path, false)
	if err != nil {
		return nil, err
	}
	return &cnlist.Result{
		CIDRs: exp.CIDRs,
		// 用文件自身的修改时间当「更新时间」：静态表没有新鲜度可言，
		// 但总得让 status 知道这份数据是什么时候放进来的。
		Source:  "ip2region 离线兜底",
		Updated: st.ModTime(),
		Offline: true,
	}, nil
}
