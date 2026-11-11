package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// initScriptPath 是 init 脚本在仓库里的源文件。
//
// 现场路由器上的是 tar.gz 版，仓库里 build-ipk.py 生成的才是源头 ——
// 以仓库版本为准改（上线时用现场那份覆盖）。
const initScriptPath = "../../scripts/build-ipk.py"

// yamlTags 列出配置结构体里所有 yaml tag（不含 omitempty 的处理，名字都要）。
func yamlTags(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(Config{})
	var out []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// Config 里嵌了 File（真正的配置项都在那儿）⇒ 必须递归下去。
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			tag := f.Tag.Get("yaml")
			if tag == "" || tag == "-" {
				continue
			}
			out = append(out, strings.Split(tag, ",")[0])
		}
	}
	walk(typ)
	return out
}

var echoKeyRe = regexp.MustCompile(`echo "([a-z_0-9]+):`)

// initScriptKeys 列出 init 脚本会翻译成 YAML 的键。
func initScriptKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(initScriptPath))
	if err != nil {
		t.Skipf("读不到 init 脚本源文件（%s）：%v", initScriptPath, err)
	}
	out := map[string]bool{}
	for _, m := range echoKeyRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}

// initScriptSkipList 是**故意**不由 init 脚本翻译的配置项。
//
// 每个都要写明理由 —— 新增配置项时如果不在这个列表里，
// 就必须同步改 init 脚本，否则「配了不生效且没有任何提示」。
var initScriptSkipList = map[string]string{
	"schema_version": "由 init 脚本固定写死 1",
	"network_key":    "由 init 脚本从 UCI 取（slx_get network_key）",
}

// TestInitScriptTranslatesAllConfigKeys 防止「加了配置项却忘了改 init 脚本」。
//
// 审计报告「根因 P」：config.go 有 36 个 yaml tag，
// 而现场 init 脚本只翻译了 14 个 —— 剩下的配了不生效，**且没有任何提示**。
// 最要命的是 dispatch_ifaces：分流接口只能自动识别，
// 多网桥/旁路由场景挑错接口 ⇒ 真实流量不被标记 ⇒ 境外直连泄露。
//
// 这个测试把「配置项」与「init 脚本的翻译」对起来，
// 以后新增配置项忘了改 init 脚本，测试就会失败。
func TestInitScriptTranslatesAllConfigKeys(t *testing.T) {
	translated := initScriptKeys(t)
	if len(translated) == 0 {
		t.Skip("init 脚本里没解析出任何键，跳过")
	}

	var missing []string
	for _, tag := range yamlTags(t) {
		if translated[tag] {
			continue
		}
		if _, ok := initScriptSkipList[tag]; ok {
			continue
		}
		missing = append(missing, tag)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("这些配置项在 init 脚本里没有翻译（配了不生效且无提示）：\n  %s\n"+
			"修法：在 scripts/build-ipk.py 的 gen_yaml() 里补上对应的一行。\n"+
			"（确实不需要翻译的，请加进 initScriptSkipList 并写明理由。）",
			strings.Join(missing, "\n  "))
	}
}

// TestInitScriptNumericValuesAreValidated 确认数值型配置项走了校验。
//
// 审计报告「根因 O」：init 脚本把 `relay_channel_failures: 8s` 这种
// 带单位的值原样写进 YAML ⇒ yaml.v3 报 "cannot unmarshal !!str into int"
// ⇒ config.go 直接返回错误 ⇒ run 退出 ⇒ procd 一小时重启 5 次就放弃
// ⇒ **网关彻底起不来且不会自动恢复**。
//
// 所以数值项必须走 slx_num() 校验，而不是直接 slx_get()。
func TestInitScriptNumericValuesAreValidated(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(initScriptPath))
	if err != nil {
		t.Skipf("读不到 init 脚本源文件（%s）：%v", initScriptPath, err)
	}
	text := string(raw)

	// 这些键在 config.go 里是 int 类型 ⇒ init 脚本必须用 slx_num 取。
	intKeys := []string{
		"relay_tls_port", "relay_tls_conns", "relay_channel_failures",
		"relay_cert_days", "listen_port", "ctrl_port", "probe_bytes",
		"egress_capacity",
	}
	for _, k := range intKeys {
		if !strings.Contains(text, "slx_num "+k) {
			t.Errorf("配置项 %s 是数值型，init 脚本必须用 slx_num 取值（当前没有）", k)
		}
	}
}
