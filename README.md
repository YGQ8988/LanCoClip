# LanCoClip

一个用 Go 编写的本地剪贴板历史服务：程序启动后持续监听电脑剪贴板，把每次复制的内容记录下来，并通过 HTTP 页面提供给手机（H5）查看。

## 功能

- 启动通知：程序就绪后弹一条系统通知（「LanCoClip已就绪 · 点击打开剪贴板记录页面」），点击横幅直接打开记录页面。macOS 走 `UNUserNotificationCenter`（首次启动会请求通知权限，仅 .app 安装方式可用）；Windows 走托盘气泡通知（Win10/11 渲染为 toast），点击气泡打开页面。
- 自动记录：按系统剪贴板的**变更事件**捕获，而不是定时抽样——macOS 读 NSPasteboard `changeCount`（40ms 内变化即可发现）、Windows 监听 `WM_CLIPBOARDUPDATE`，文本与图片都走原生 API 读取（不再为每次探测启动 `pbpaste` / PowerShell 子进程）。因此连续快速复制时每一条都会各自入库，不会只剩最后一条。其他平台（Linux）退回定时轮询。新内容自动入库，最多保留 500 条（文本 / 图片条数均可在设置中调整），去重相邻重复内容。
- 文本 + 图片：复制的截图/图片同样会被记录，存为独立 PNG 文件，页面显示缩略图并可「保存」。纯文本优先于图片（部分应用复制文本时会附带图像渲染，如 Excel 单元格），因此有文本时不会记成截图。
- 不记录「页面复制」：在服务页面上点「复制」/「复制链接」，或手动选中文字复制、右键复制图片时，页面会先上报服务端做**一次性抑制**，这段内容不会再被记进历史，避免产生重复记录。手机上复制走的是手机自己的剪贴板，不受影响、也不上报。
- 顶部 tab：**文本 / 图片** 两个 tab 分开展示与分页，tab 上显示各自数量；两者**分开存储**（`texts.json` / `images.json`）、分开计数、分开分页，互不影响。清空按钮跟随当前 tab（「清空全部文本」/「清空全部图片」），只清当前类型。
- 图片等高网格：图片 tab 的缩略图**固定高度**（PC 200px、H5 240px，按主流屏幕尺寸 1920×1080 四列 / 390×844 单列分别取值），`object-fit:contain` 完整显示不裁切，网格不再一高一低；图片完整高度超出固定高度时显示「展开」看全图，PC 与 H5 共用同一套判断逻辑。
- 保留条数可设置：工具栏右上角**设置按钮**（齿轮图标，仅服务本机可见，与「清空全部」同权限模型）打开设置弹窗，可分别设置**文本 / 图片保留条数**（文本 10–5000、图片 5–1000，默认 500 / 200），保存后**立即生效**：新上限即刻约束后续新增，已有记录超出时**优先删除最早的记录**（图片同时删除文件），配置落盘 `settings.json`，重启不丢。
- 权限控制：**访问来源 IP 等于服务本机 IP（回环或本机网卡地址）时才能"清空全部记录"，其他设备访问为只读；手机端一律不展示「清空全部」按钮**——服务端按 User-Agent + `Sec-CH-UA-Mobile` 判定，前端再按 `maxTouchPoints` / `pointer: coarse` 判定并把按钮从 DOM 移除，因此即使代理改写 UA、或浏览器开了「请求桌面版网站」也不会误判。
- 页面防缓存：HTML 响应带 `no-store`，避免浏览器或中间代理（如 Whistle）缓存旧页面导致修复不生效。
- 访问地址展示：页面顶部展示局域网访问链接，旁边是「分享二维码」图标按钮与「复制链接」图标按钮（均为纯图标）；点击二维码弹出 URL 二维码弹窗（支持 ESC / 点击遮罩 / 右上角 × 关闭）。
- 访问地址选路：优先取 **en0（网卡1）** 的 IPv4，跳过 utun / bridge / vmnet / docker 等虚拟网卡，避免手机上打不开。
- 中文编码：`.app` 从访达启动时没有 locale，`pbpaste` 会退回系统遗留编码（如 GB18030）导致乱码，程序强制 `LC_CTYPE=UTF-8` 并在必要时按 GB18030 兜底解码。
- 列表展示：时间统一为 `YYYY-MM-DD HH:mm:ss`；时间与「展开」/「复制」（图片为「保存」）**同排显示**，卡片紧凑、一屏能放更多条；内容超过一行默认收起，点击「展开」才显示完整内容。
- 图片收起：图片缩略图固定高度（PC 200px / H5 240px，见「图片等高网格」），contain 完整显示；完整高度超出固定高度才显示「展开」。
- 分页：PC **每页 32 条**（4 列 × 8 行），H5 每次上滑追加 10 条。PC 为页码分页（点击页码跳转、上一页 / 下一页，页数多时用省略号收拢，列表与翻页器之间有分隔线并留出底部留白）；H5 滑到底显示「没有更多数据了」。
- 多列布局：PC **宽屏四列**（≥1040px），中等窗口两列（≥820px），更窄与 H5 单列；各卡保持自身高度，不被同行卡片拉伸。
- 增量轮询：前端每 2 秒只探测第 1 页是否有新数据，有变化才重载当前视图，既省流量又不打断阅读。
- H5 适配：移动端优先布局、毛玻璃样式、安全区适配、复制/展开等触屏友好交互。
- 主题跟随系统：通过 `prefers-color-scheme` 自动切换浅色 / 深色，无需手动设置。
- 数据持久化：每次复制**立即落盘**到用户配置目录的 `LanCoClip/`（文本 `texts.json`、图片元数据 `images.json`、图片原图 `images/`），退出、重启或重新安装都不会丢记录。
- 版本可见：设置弹窗标题旁显示当前构建版本与构建时间（由 `build-mac.sh` / `build-windows.sh` 注入），方便确认正在运行的是哪个包。
- 常驻应用：macOS 打包成 `.app`（菜单栏图标，Dock 不显示）、Windows 打包成 `.exe`（任务栏通知区域），点开菜单均可「打开页面」或「退出」。

## 构建与运行

### 命令行方式

```bash
go build -o lanoclip .
./lanoclip                 # 默认端口 6116
./lanoclip -port 8080      # 指定端口
./lanoclip -interval 300ms # 调整剪贴板轮询间隔
./lanoclip -tray           # 以菜单栏模式运行（macOS）
./lanoclip -data /tmp/x    # 指定数据目录（隔离测试用，不动真实数据）
```

数据目录可用环境变量 `LANOCLIP_DIR` 指定（优先级低于 `-data`）。**自测/调试请务必用隔离目录**，避免把测试文本写进真实记录。

启动后终端会打印手机可访问的地址，例如 `http://192.168.x.x:6116`。手机与电脑连同一 Wi-Fi，扫码或直接输入该地址即可查看；在电脑本机打开该地址可清空记录。

若默认端口被占用（例如被 Whistle 代理占用），程序会**自动顺延到下一个空闲端口**并按实际端口生成访问地址。

### macOS 菜单栏应用

```bash
./build-mac.sh          # 默认版本 1.0.2
./build-mac.sh 1.0.3    # 指定版本号
```

产物在 `dist/`：

- `LanCoClip.app` — 本机架构，双击即用
- `LanCoClip-<version>-mac-arm64.zip` — Apple Silicon
- `LanCoClip-<version>-mac-x64.zip` — Intel

双击 `.app` 后菜单栏出现图标，点击（右键）可「打开页面」或「退出」。首次运行若被 Gatekeeper 拦截：`xattr -dr com.apple.quarantine dist/LanCoClip.app`。

图标资源由 `go run ./tools/genicons` 生成（`-from <源图>` 可从自定义图片派生），脚本会在缺失时自动生成：macOS 菜单栏彩色图标、Windows 托盘/程序图标、应用图标、以及网页页签图标 `web/favicon.png`。构建需要 Go 与 Xcode 命令行工具（`xcode-select --install`），且必须在 Mac 本机编译（菜单栏走 cgo/Cocoa）。

### Windows 应用

```bash
./build-windows.sh          # 默认版本 1.2.2
./build-windows.sh 1.2.3    # 指定版本号
```

产物在 `dist/`，zip 内统一是 `LanCoClip.exe`（架构只体现在 zip 名）：

- `LanCoClip-<version>-windows-x64.zip` — Intel / AMD 64 位
- `LanCoClip-<version>-windows-arm64.zip` — Windows on ARM

双击运行后**常驻任务栏（通知区域）**，右键菜单为「打开页面」「退出」；用 `-H windowsgui` 编译，不会弹出黑色控制台窗口，日志写入数据目录的 `app.log`。

Windows 端托盘是纯 Go 实现，因此可在 Mac / Linux 上交叉编译（`CGO_ENABLED=0`）。exe 的图标与版本信息由 `go-winres` 生成（`rsrc_windows_*.syso` 会自动链接）；未安装该工具时会跳过并给出提示，功能不受影响（装回：`go install github.com/tc-hib/go-winres@latest`）。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | H5 页面（`no-store`，不缓存） |
| GET | `/api/records?type=text&page=1&size=10` | 分页记录，`type`=text\|image（默认 text）；返回 `records` / `type` / `page` / `size` / `total` / `totalPages` / `hasMore` / `counts`（两个 tab 的数量）/ `settings`（当前保留条数）/ `canClear` / `shareUrl` / `version`（`size` 默认 10，上限 500） |
| GET | `/api/image?id=<id>` | 图片记录的 PNG 原图 |
| POST | `/api/clear?type=text` | 清空记录，`type` 指定只清某一类，不带则全清（仅服务本机来源、且非手机可调用，否则 403） |
| POST | `/api/suppress` | 上报「这段内容是从页面复制的」，剪贴板轮询会跳过一次（`{"kind":"text","content":"…"}` 或 `{"kind":"image","id":123}`）；仅服务本机可调用 |
| GET | `/api/qr?text=<url>` | 生成 URL 二维码 PNG |
| GET | `/api/settings` | 读取当前保留条数（`{"maxText":500,"maxImage":200}`） |
| POST | `/api/settings` | 修改保留条数（`{"maxText":300,"maxImage":100}`），保存后立即生效并按需修剪最旧记录；仅服务本机来源、且非手机可调用，否则 403；返回 `trimmedText` / `trimmedImage`（本次删除的最旧记录数） |
| POST | `/api/activate` | 单实例探测：已在运行的实例自报家门 `{"ok":true,"app":"lanoclip","url":"http://127.0.0.1:<port>"}`，并弹出「LanCoClip已在运行 · 点击即可打开剪贴板记录页面」通知；新的一次启动探测到它就不再重复启动。仅服务本机可调用，非本机 403 |

## 说明

- Linux 需要 `xclip` 或 `xsel`；Windows 使用 PowerShell `Get-Clipboard`。图片读写支持 macOS（Cocoa/NSPasteboard）与 Windows（Win32 剪贴板 CF_DIB，直接调用 API 不走子进程）；Linux 暂不支持图片。
- **记录保存位置**：数据目录按优先级取 `-data` 参数 > 环境变量 `LANOCLIP_DIR` > 各平台默认用户配置目录下的 `LanCoClip/`（早期版本用过别的目录名，现已统一为 `LanCoClip/`，旧目录不再自动读取）：

  | 平台 | 数据目录 |
  | --- | --- |
  | macOS | `~/Library/Application Support/LanCoClip/` |
  | Windows | `%APPDATA%\LanCoClip\`（即 `C:\Users\<用户名>\AppData\Roaming\LanCoClip\`） |
  | Linux | `~/.config/LanCoClip/`（设置了 `XDG_CONFIG_HOME` 时为 `$XDG_CONFIG_HOME/LanCoClip/`） |

  目录内容：`texts.json`（文本记录）、`images.json`（图片记录元数据）、`images/`（图片原图 PNG，文件名即记录 id）、`settings.json`（保留条数 / 快捷键等配置）、`app.log`（运行日志，托盘模式无终端时排查问题用）。备份记录只需拷贝整个目录；换机器迁移时把 `images/` 和两个 json 一起拷过去即可。
- 旧版本的单文件 `history.json` 会在首次启动时**自动拆分迁移**为上面两个文件，并改名为 `history.json.migrated` 保留（不丢数据）。
- 上限：文本默认 500 条、图片默认 200 条，可在页面右上角「设置」中调整（文本 10–5000、图片 5–1000），超出时淘汰最旧的（图片会同时删除文件）。
- 页面行为可用 Playwright 做真实设备模拟自测（iPhone UA、iPhone + 桌面版 UA、桌面三种场景）。
