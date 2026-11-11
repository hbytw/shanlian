// Package buildinfo 承载编译期注入的版本信息。
//
// 通过链接器注入（见 scripts/build-all.sh）：
//
//	go build -ldflags "-X slx/internal/buildinfo.Version=1.0.0 ..."
package buildinfo

var (
	// Version 语义化版本号，形如 "1.0.0"。
	Version = "0.0.0-dev"
	// Commit 构建时的 git 提交短哈希。
	Commit = "unknown"
	// BuildTime 构建时间（UTC RFC3339）。
	BuildTime = "unknown"
	// ProtoMin / ProtoMax 本二进制支持的最小/最大线协议版本。
	//
	// 版本协商规则见 internal/proto：双方取 [max(min), min(max)] 区间，
	// 交集为空则拒绝连接并给出清晰日志。
	ProtoMin = 1
	ProtoMax = 1
)

// String 返回可读的版本串。
func String() string {
	return Version + " (" + Commit + ", " + BuildTime + ")"
}
