package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pidFilePath 返回守护进程 pid 文件路径。
//
// 放 /tmp 而不是 /var/run：OpenWrt 的 /var 是 tmpfs 且可能没建好，
// /tmp 在所有目标系统上都必定可写。
func pidFilePath() string {
	if p := os.Getenv("SLX_PID_FILE"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "slx.pid")
}

// writePIDFile 记录当前进程 pid。
func writePIDFile() error {
	path := pidFilePath()
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// removePIDFile 清理 pid 文件（只删自己的）。
func removePIDFile() {
	path := pidFilePath()
	if pid, err := readPIDFile(); err == nil && pid != os.Getpid() {
		return // 已经被别的进程接管了，别删
	}
	_ = os.Remove(path)
}

// readPIDFile 读取守护进程 pid。
func readPIDFile() (int, error) {
	b, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("pid 文件内容非法: %w", err)
	}
	return pid, nil
}
