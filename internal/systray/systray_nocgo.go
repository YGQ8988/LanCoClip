//go:build !windows && !(darwin && cgo)

package systray

// 没有原生托盘后端时的纯 Go 兜底实现（例如 CGO_ENABLED=0 的静态分析 /
// 交叉编译）。这个包以前只在 cgo 打开时才存在，导致 `CGO_ENABLED=0
// GOOS=darwin go build ./...`、`go vet` 和任何不带 cgo 的 CI 直接编译失败。
// 这里把桥接函数做成空实现：托盘不显示，但程序其余部分照常编译、照常运行。

func nativeLoop()      {}
func registerSystray() {}
func quit()            {}

// SetIcon is a no-op without a native tray backend.
func SetIcon([]byte) {}

// SetTitle is a no-op without a native tray backend.
func SetTitle(string) {}

// SetTooltip is a no-op without a native tray backend.
func SetTooltip(string) {}

func addOrUpdateMenuItem(*MenuItem) {}
func addSeparator(uint32)           {}
func hideMenuItem(*MenuItem)        {}
func showMenuItem(*MenuItem)        {}
