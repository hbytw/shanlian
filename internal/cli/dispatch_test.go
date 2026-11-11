package cli

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"slx/internal/cnlist"
)

// syncBuffer 是并发安全的日志缓冲：后台 goroutine 写，测试线程读。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAuditDisabledByDefault 确认后台交叉校验默认是关的。
//
// 默认关闭是有道理的：审计要枚举约 55 万个网段，峰值内存上百 MB，
// 在小内存路由器上足以把 ZeroTier 饿死（见 TestAuditNeverRunsOnStartupPath）。
// 想看数据源分歧，人工跑 slx audit-cn 就行。
func TestAuditDisabledByDefault(t *testing.T) {
	t.Setenv("SLX_AUDIT_CN", "")
	if auditEnabled() {
		t.Fatal("后台审计默认应当关闭")
	}
}

// TestAuditEnvParsing 确认开关的各种写法。
func TestAuditEnvParsing(t *testing.T) {
	cases := map[string]bool{
		"":       false,
		"0":      false,
		"false":  false,
		"no":     false,
		"off":    false,
		"1":      true,
		"true":   true,
		"yes":    true,
		"  on  ": true,
	}
	for v, want := range cases {
		t.Setenv("SLX_AUDIT_CN", v)
		if got := auditEnabled(); got != want {
			t.Errorf("SLX_AUDIT_CN=%q → %v，期望 %v", v, got, want)
		}
	}
}

// TestStartBackgroundAuditDoesNotBlock 是「启动路径不等待 audit 完成」的回归测试。
//
// 事故当天审计在启动路径上同步跑了 1.6 秒，随后 25 秒里 ZeroTier 一直不响应
// keepalive，所有出口 agent 集体掉线。现在审计要么不做，要么在后台 goroutine 里做，
// 启动路径绝不等待它 —— 这个测试把这条性质钉死。
func TestStartBackgroundAuditDoesNotBlock(t *testing.T) {
	t.Setenv("SLX_AUDIT_CN", "1")

	// 把后台延迟调到 0，这样能观测到后台线程确实跑起来了。
	old := auditStartDelay
	auditStartDelay = 0
	defer func() { auditStartDelay = old }()

	var buf syncBuffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	res := &cnlist.Result{CIDRs: []string{"1.0.0.0/24", "1.0.1.0/24"}}

	start := time.Now()
	startBackgroundAudit(context.Background(), res, log)
	elapsed := time.Since(start)

	// 启动路径不能等审计：返回必须是「立刻」，而不是「审计跑完」。
	if elapsed > 200*time.Millisecond {
		t.Fatalf("启动路径等待了审计完成，耗时 %v —— 这正是线上事故的形态", elapsed)
	}

	// 后台线程最终要打出一条 started 事件（证明它是异步跑的，不是被跳过了）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "dispatch.cnlist_audit_started") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("后台审计没有启动，日志内容: %s", buf.String())
}

// TestAuditNeverRunsOnStartupPath 是这次线上事故的回归护栏。
//
// 事故原因：setupDispatch 同步调用了 auditCNList —— 它要枚举 ip2region xdb 里
// 约 55 万个网段并做区间运算，在小内存路由器上产生巨大的内存/CPU 峰值，
// 把 ZeroTier 守护进程饿死（ARP 应答超时、keepalive 超时），
// 20 秒内所有出口 agent 集体掉线。
//
// 护栏规则：
//  1. 启动路径上的函数不得同步调用 auditCNList / ipgeo.AuditCN；
//  2. auditCNList 的调用点必须包在 go 语句里（即后台执行）。
//
// 用源码扫描来守，是因为这个性质没法用普通单元测试表达：
// 只要有人再把 auditCNList 塞回 setupDispatch，这个测试立刻红。
func TestAuditNeverRunsOnStartupPath(t *testing.T) {
	sites, err := collectCallSites(".")
	if err != nil {
		t.Fatalf("扫描源码失败: %v", err)
	}

	// 启动路径上的函数：这些函数跑在守护进程启动的过程里。
	startupFns := map[string]bool{
		"setupDispatch":   true,
		"setupDataPlane":  true,
		"setupProxyPlane": true,
		"serve":           true,
		"serveProxy":      true,
		"Run":             true,
	}

	sawAudit := false
	for _, s := range sites {
		isAudit := s.name == "auditCNList" || s.name == "AuditCN"
		if !isAudit {
			continue
		}
		sawAudit = true
		if startupFns[s.fn] {
			t.Errorf("启动路径函数 %s 同步调用了 %s —— "+
				"审计会把小内存路由器上的 ZeroTier 饿死（线上事故）", s.fn, s.name)
		}
		if s.name == "auditCNList" && !s.inGo {
			t.Errorf("auditCNList 在 %s 里被同步调用，必须放进 go 语句里后台执行", s.fn)
		}
	}
	if !sawAudit {
		t.Fatal("没找到任何审计调用点 —— 护栏失效，请确认 auditCNList 还在（slx audit-cn 依赖它）")
	}
}

// callSite 是源码里的一处函数调用。
type callSite struct {
	fn   string // 所在函数
	name string // 被调用的函数名
	inGo bool   // 是否位于 go 语句内部
}

// collectCallSites 扫描目录下的 Go 文件，收集函数调用点。
func collectCallSites(dir string) ([]callSite, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []callSite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			out = append(out, callsInFunc(fd)...)
		}
	}
	return out, nil
}

// callsInFunc 收集一个函数里的调用点，并标记是否位于 go 语句内。
func callsInFunc(fd *ast.FuncDecl) []callSite {
	var out []callSite
	goDepth := 0
	var stack []ast.Node

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			// 退出一个节点：ast.Inspect 会在访问完子节点后回调 nil。
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := top.(*ast.GoStmt); ok {
				goDepth--
			}
			return false
		}
		stack = append(stack, n)
		switch v := n.(type) {
		case *ast.GoStmt:
			goDepth++
		case *ast.CallExpr:
			out = append(out, callSite{
				fn:   fd.Name.Name,
				name: calleeName(v.Fun),
				inGo: goDepth > 0,
			})
		}
		return true
	})
	return out
}

// calleeName 取出被调用函数的名字（取最后一段标识符）。
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
