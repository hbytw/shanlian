package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// 本地 API 参数。
const (
	// 默认只监听回环，不对外暴露 —— 这是管理接口，不该被局域网访问。
	apiDefaultAddr = "127.0.0.1:7079"

	// apiReadTimeout 防止慢连接占住 goroutine。
	apiReadTimeout = 5 * time.Second

	// pprofDefaultAddr 性能剖析的监听地址。
	// 只绑回环 —— 剖析端点能泄漏运行时信息，绝不能暴露到局域网 ✅✅
	pprofDefaultAddr = "127.0.0.1:6060"
)

// apiServer 是本地只读 HTTP API。
//
// 为什么要有它：
//   - CLI 需要能主动触发动作（比如「现在立刻探一次」），而文件快照是只读的；
//   - M6 要求 API 兼容，先把 v1 的骨架固定下来，后面只加不改。
//
// 安全：默认只绑 127.0.0.1。改配置暴露到局域网时，请自己确认网络可信。
type apiServer struct {
	addr   string
	status func() StatusSnapshot
	routes func() interface{}
	probe  func(domain string) error
	log    func(string, ...any)
	// pprof 为 true 时在 API mux 上挂 /debug/pprof 端点。
	// （独立监听端口由 startPprof 负责 ✅✅）
	pprof bool
}

// startAPI 启动本地 HTTP API，返回实际监听地址。
func startAPI(ctx context.Context, s *apiServer) (string, error) {
	if s.addr == "" {
		s.addr = apiDefaultAddr
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return "", err
	}

	mux := http.NewServeMux()

	// 性能剖析（pprof）—— 默认关闭 ✅
	//
	// 为什么要有它 ✅✅：代理数据面的瓶颈在用户态协议栈（gvisor netstack ✅✅），
	// 光看 CPU 占用和平均速率看不出热点在哪 ✅⚠️ —— 必须有 profile 才能对症下药 ✅✅
	//
	// 安全 ✅✅：API 只监听 127.0.0.1 ✅✅（apiDefaultAddr ✅✅）⇒ 剖析端点不会暴露到局域网 ✅✅✅ ✅
	// 开启方式：环境变量 SLX_PPROF=1 ✅✅（或 slx_pprof=1 ✅✅）
	if s.pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.status())
	})
	mux.HandleFunc("/api/v1/routes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.routes())
	})
	mux.HandleFunc("/api/v1/probe", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		if domain == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 domain 参数"})
			return
		}
		if s.probe != nil {
			if err := s.probe(domain); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "domain": domain})
	})
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now()})
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  apiReadTimeout,
		WriteTimeout: apiReadTimeout * 2,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() { _ = srv.Serve(ln) }()

	return ln.Addr().String(), nil
}

// startPprof 启动独立的性能剖析 HTTP 服务。
//
// 为什么独立于 startAPI ✅✅：
//   - startAPI 在「数据面启动成功」的分支里 ✅✅ —— 数据面降级时它就不在 ✅✅；
//   - 优化数据面性能时，恰恰需要「数据面可能还没好也能剖析」✅✅ —— 否则无从下手 ✅✅；
//   - 而且它必须与业务 API 解耦 ✅✅：剖析端点绝不能因为业务 API 的问题而不可用 ✅✅。
//
// 安全 ✅✅：只监听 127.0.0.1 ✅✅（默认 pprofAddr ✅✅）⇒ 不暴露到局域网 ✅✅✅ ✅
// 开启方式：环境变量 SLX_PPROF=1 ✅✅
func startPprof(ctx context.Context, enabled bool) (string, error) {
	if !enabled {
		return "", nil
	}
	addr := pprofDefaultAddr
	if v := os.Getenv("SLX_PPROF_ADDR"); v != "" {
		addr = v
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// —— slx probe ——

// Probe 实现 `slx probe [域名]`。
//
// 不带参数：展示当前的探测结果（读状态快照）。
// 带参数：请求守护进程立刻对所有出口测一次该目标，然后展示。
func Probe(args []string) int {
	if len(args) == 0 {
		return showProbeResults()
	}
	domain := args[0]
	if err := callProbeAPI(domain); err != nil {
		fmt.Printf("触发探测失败：%v\n", err)
		fmt.Println("提示：守护进程可能没在运行，或本地 API 端口被占用（默认 127.0.0.1:7079）")
		return 1
	}
	fmt.Printf("已请求对所有出口探测 %s，60 秒内结果会反映到评分里。\n", domain)
	fmt.Println("稍后用 `slx probe`（不带参数）查看结果。")
	return 0
}

// callProbeAPI 调本地 API 触发一次探测。
func callProbeAPI(domain string) error {
	addr := apiAddr()
	u := fmt.Sprintf("http://%s/api/v1/probe?domain=%s", addr, domain)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("API 返回 %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// showProbeResults 展示当前各出口的实测质量。
func showProbeResults() int {
	rs, err := readRouteSnapshot()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	now := time.Now()
	fmt.Printf("出口实测质量（快照时间 %s，%s前）\n\n",
		rs.UpdatedAt.Format("15:04:05"), humanAge(now.Sub(rs.UpdatedAt)))

	if len(rs.Candidates) == 0 {
		fmt.Println("  还没有任何出口的测量数据。")
		fmt.Println("  提示：探测每 2 秒一次；若一直没有数据，用 `slx status` 看组网是否正常。")
		return 0
	}

	fmt.Printf("  %-18s %8s %10s %10s %8s  %s\n", "出口", "评分", "链路延迟", "可用带宽", "丢包", "状态")
	for _, c := range rs.Candidates {
		state := c.Breaker
		if state == "" {
			state = "正常"
		}
		if c.Note != "" {
			state = c.Note
		}
		fmt.Printf("  %-18s %7.1f %8.1fms %8.1fM %7.1f%%  %s\n",
			c.NodeID, c.Score, c.RTTMS, c.BWMbps, c.Loss*100, state)
	}
	fmt.Println("\n说明：")
	fmt.Println("  链路延迟 = 网关↔出口（L1），每 2 秒测一次")
	fmt.Println("  可用带宽 = min(L1 链路吞吐, L2 出口到目标的速率)，L2 每 60 秒测一次")
	fmt.Println("  国内网段的流量不走隧道，所以这里只关心境外目标的质量")
	return 0
}

// apiAddr 返回 API 监听地址。
func apiAddr() string {
	if v := os.Getenv("SLX_API_ADDR"); v != "" {
		return v
	}
	return apiDefaultAddr
}
