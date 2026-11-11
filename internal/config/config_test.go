package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.yaml")
}

func TestLoadCreatesWithKey(t *testing.T) {
	p := tmpPath(t)
	c, res, err := Load(p)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !res.Created {
		t.Error("应标记为新建")
	}
	if res.GeneratedKey == "" {
		t.Error("应生成密钥")
	}
	if c.NetworkKey != res.GeneratedKey {
		t.Error("配置里的密钥应与生成的一致")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("新建配置应通过校验: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("配置文件应已落盘: %v", err)
	}
}

func TestCreatedFileIsMinimal(t *testing.T) {
	// 这是「配置项只有 2 个」的守卫：新建的文件里不应出现
	// 用户没设置过的可选项。默认值只在运行时生效。
	p := tmpPath(t)
	if _, _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, forbidden := range []string{"log_level:", "log_format:", "listen_addr:", "probe_bytes:", "listen_port:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("新建配置不应包含默认项 %q\n文件内容:\n%s", forbidden, body)
		}
	}
	for _, required := range []string{"schema_version:", "network_key:"} {
		if !strings.Contains(body, required) {
			t.Errorf("新建配置缺少必需项 %q\n文件内容:\n%s", required, body)
		}
	}
}

func TestLoadIsStable(t *testing.T) {
	p := tmpPath(t)
	c1, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c2, res2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Created {
		t.Error("第二次加载不应标记为新建")
	}
	if res2.GeneratedKey != "" {
		t.Error("第二次加载不应重新生成密钥")
	}
	if c1.NetworkKey != c2.NetworkKey {
		t.Error("密钥应保持稳定")
	}
}

func TestKeyFormat(t *testing.T) {
	p := tmpPath(t)
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.KeyBytes()); got != KeyLen {
		t.Errorf("密钥解码后应为 %d 字节，得到 %d", KeyLen, got)
	}
}

func TestValidateRejectsBadKey(t *testing.T) {
	cases := []string{
		"not-base64!!!",
		"",     // 空
		"YWJj", // 只有 3 字节
	}
	for _, k := range cases {
		c := &Config{File: File{NetworkKey: k}}
		if err := c.Validate(); err == nil {
			t.Errorf("密钥 %q 应被拒绝", k)
		}
	}
}

func TestEffDefaults(t *testing.T) {
	// 未设置时访问器必须给出默认值，这是「零配置启动」的基础。
	c := &Config{}
	if c.EffLogLevel() != "info" {
		t.Errorf("默认日志级别 = %q", c.EffLogLevel())
	}
	if c.EffLogFormat() != "json" {
		t.Errorf("默认日志格式 = %q", c.EffLogFormat())
	}
	if c.EffListenAddr() != "0.0.0.0" {
		t.Errorf("默认监听地址 = %q", c.EffListenAddr())
	}
	if c.EffProbeBytes() != 2<<20 {
		t.Errorf("默认探测字节数 = %d", c.EffProbeBytes())
	}
}

func TestEffOverrides(t *testing.T) {
	c := &Config{File: File{LogLevel: "debug", ListenAddr: "127.0.0.1", ProbeBytes: 1024}}
	if c.EffLogLevel() != "debug" {
		t.Errorf("覆盖值未生效: %q", c.EffLogLevel())
	}
	if c.EffListenAddr() != "127.0.0.1" {
		t.Errorf("覆盖值未生效: %q", c.EffListenAddr())
	}
	if c.EffProbeBytes() != 1024 {
		t.Errorf("覆盖值未生效: %d", c.EffProbeBytes())
	}
}

func TestEgressCapacityScalesWithMemory(t *testing.T) {
	// 低内存设备必须自动缩容，否则会 OOM（DESIGN.md §12.4）
	c := &Config{}
	small := c.EffEgressCapacity(64 * 1024)       // 64 MB
	large := c.EffEgressCapacity(4 * 1024 * 1024) // 4 GB
	if small >= large {
		t.Errorf("小内存容量应小于大内存: %d vs %d", small, large)
	}
	if small < 64 {
		t.Errorf("容量不应低于下限 64，得到 %d", small)
	}
	if large > 65536 {
		t.Errorf("容量不应超过上限 65536，得到 %d", large)
	}
	if got := c.EffEgressCapacity(0); got != 1024 {
		t.Errorf("未知内存时应回落到 1024，得到 %d", got)
	}
}

func TestMigrateFutureVersionRejected(t *testing.T) {
	// 配置来自更新版本的程序 → 必须明确报错，而不是静默降级破坏数据
	f := &File{SchemaVersion: CurrentSchema + 1}
	if _, err := migrate(f); err == nil {
		t.Error("高于当前版本的配置应被拒绝")
	}
}

func TestMigrateOldVersion(t *testing.T) {
	f := &File{SchemaVersion: 1}
	from, err := migrate(f)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if from != 0 {
		t.Errorf("同版本不应标记迁移，得到 %d", from)
	}
	if f.SchemaVersion != CurrentSchema {
		t.Errorf("版本应保持 %d，得到 %d", CurrentSchema, f.SchemaVersion)
	}
}

func TestSaveRoundtripWithOptionalFields(t *testing.T) {
	p := tmpPath(t)
	c := &Config{
		File: File{
			SchemaVersion: CurrentSchema,
			NetworkKey:    mustKey(t),
			ProbeTargets:  []string{"nodejs.org", "cdn.jsdelivr.net"},
			LogLevel:      "debug",
		},
		Path: p,
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	got, _, err := Load(p)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if got.LogLevel != "debug" {
		t.Errorf("用户显式设置的 log_level 应被保留，得到 %q", got.LogLevel)
	}
	if len(got.ProbeTargets) != 2 {
		t.Errorf("probe_targets 应被保留，得到 %v", got.ProbeTargets)
	}
}

func mustKey(t *testing.T) string {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}
