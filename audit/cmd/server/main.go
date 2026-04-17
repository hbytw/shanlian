//! 闪连 VPN 审计服务器
//!
//! 负责：
//! - 接收 Planet 日志
//! - 存储和查询日志
//! - 流量统计聚合
//! - 告警触发

package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shanlian/audit/internal/alert"
	"github.com/shanlian/audit/internal/forward"
	"github.com/shanlian/audit/internal/receiver"
	"github.com/shanlian/audit/internal/storage"
	"github.com/valyala/fasthttp"
)

func main() {
	// 命令行参数
	listenAddr := flag.String("listen", "0.0.0.0:8090", "监听地址")
	dbPath := flag.String("db", "./audit.db", "SQLite 数据库路径")
	flag.Parse()

	// 初始化存储
	store, err := storage.NewSQLiteStorage(*dbPath)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}
	defer store.Close()

	// 初始化告警引擎
	alertEngine := alert.NewEngine(store)

	// 初始化转发管理器
	forwardMgr := forward.NewManager(store)

	// 启动后台任务
	store.StartAggregationTicker()
	alertEngine.StartAlertChecker()

	// 创建 HTTP 处理器
	handler := receiver.NewHandler(store, alertEngine, forwardMgr)

	// 启动服务器
	log.Printf("审计服务器启动，监听 %s", *listenAddr)

	// 处理优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("收到退出信号，正在关闭...")
		store.Stop()
		os.Exit(0)
	}()

	if err := fasthttp.ListenAndServe(*listenAddr, handler.Handle); err != nil {
		log.Fatalf("服务器错误: %v", err)
	}
}
