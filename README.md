<div align="center">

<img src="web/appicon.png" alt="LanCoClip" width="120" />

# LanCoClip

用 Go 写的**本地剪贴板历史服务**：后台常驻，把每一次复制（文本 / 图片）记下来，手机连同一 Wi-Fi 打开网页就能看、能复制回来。

</div>

仓库：<https://github.com/YGQ8988/LanCoClip>

## 平台能力

| 平台 | 变更捕获 | 图片 | 菜单栏 / 托盘 | 系统通知 | 开机自启 |
| --- | --- | --- | --- | --- | --- |
| macOS | `NSPasteboard` `changeCount`（事件驱动） | ✅ | ✅ 菜单栏 | ✅ | ✅ LaunchAgent |
| Windows | `WM_CLIPBOARDUPDATE`（事件驱动） | ✅（Win32 `CF_DIB`） | ✅ 通知区域 | ✅ | ✅ 注册表 Run 键 |
| Linux | 定时轮询（`xclip` / `xsel`） | ❌ | ❌ | ❌ | ❌ |

Linux 可以跑命令行模式记录文本，但没有托盘、通知和自启。

## 功能

### 记录

- **事件驱动，不是定时抽样**：macOS 读 `NSPasteboard` 的 `changeCount`，Windows 监听 `WM_CLIPBOARDUPDATE`，文本与图片都走原生 API（不再为每次探测启动 `pbpaste` / PowerShell 子进程）。连续快速复制时每一条都会各自入库，不会只剩最后一条。
- **文本 + 图片**：截图 / 复制的图片存为独立 PNG，页面显示缩略图，可「保存」下载原图。
- **文本优先于图片**：Excel 等应用复制单元格时会同时塞一份渲染图，有文本时不会记成截图。
- **相邻重复内容去重**，同一个内容反复复制只留一条。
- **立即落盘**（另加 3 秒兜底 flush），程序被强杀也不会丢刚复制的内容。
- **不记录「从页面复制」**：在页面点「复制」「保存」「复制链接」，页面会先上报服务端做一次性抑制，这次变更不再入库，避免自产自销的重复记录。手机上复制走手机自己的剪贴板，不受影响。
- **上限可配**：文本默认 500 条（10–5000）、图片默认 200 条（5–1000），超出时淘汰最旧的（图片同时删除文件）。
- **清空后 ID 从 1 重新数**：文本与图片各自编号，清空哪一类就只重置那一类（图片文件名即 `<id>.png`，重号不会覆盖还在用的图）。
- **中文编码兜底**：`.app` 从访达启动没有 locale，`pbpaste` 会退回系统遗留编码导致乱码，程序强制 `LC_CTYPE=UTF-8`，必要时按 GB18030 解码。

### 网页

- **文本 / 图片** 两个 tab，tab 上显示各自数量，分开存储、分开分页。
- **PC 分页**：每页 32 条（4 列 × 8 行），页码跳转、上一页 / 下一页，超过 7 页用省略号收拢。**H5 上滑加载**：每次追加 10 条，到底提示「没有更多数据了」。
- **图片等高网格**：PC 200px、H5 240px，`object-fit: contain` 完整显示不裁切；超过固定高度才显示「展开」看全图。
- 文本卡片可**复制**（>1 行默认收起，点「展开」看全文）；图片卡片可**复制**到剪贴板（桌面端）、**保存** PNG。
- **反向推送**（手机 / PC 都可用）：右下角悬浮按钮跟着 tab 变——文本 tab 弹输入框（底部「取消 / 添加」）直接往电脑写一条文本；图片 tab 唤起系统相册 / 文件管理器，选中的图上传后转 PNG 入库（JPEG / GIF 会自动转换）。按钮可**拖动**到任意位置（避开遮挡），位置记在 localStorage，转屏 / 改窗口后自动夹回可视区。
- **访问地址卡片**：顶部展示局域网链接，旁边是「二维码」与「复制链接」图标按钮；二维码弹窗支持 ESC / 点遮罩 / 右上角 × 关闭。局域网 IP 优先取 **en0**，跳过 utun / bridge / vmnet / docker 等虚拟网卡。
- **设置弹窗**（仅服务本机可见）：「通用」页可开**开机自启**、分别设**文本 / 图片保留条数**；「快捷键」页可点框捕获按键自定义（Esc 取消，「还原」丢弃未保存）。标题旁显示**版本号与构建时间**。
- **增量轮询**：每 2 秒只比对第 1 页签名，有变化才刷新；H5 走顶部增量插入，不打断滚动、不重置位置。断连会 toast 提示「连接断开，重试中…」。
- **主题**：默认跟随系统（`prefers-color-scheme`），可手动切换并记住。
- **权限**：只有来源 IP 等于服务本机（回环或本机网卡地址）时才能清空 / 改设置；手机端直接不渲染这些按钮（UA + 触屏双判定），非本机调用返回 403。
- **URL 参数**：`?qr=1` 直接弹二维码、`?settings=1` 弹设置、`?history=1` 打开历史浮层；关闭弹窗后参数自动抹掉。
- 页面响应带 `no-store`，避免浏览器或代理（如 Whistle）缓存旧页面。

### 桌面集成

- **菜单**（macOS 菜单栏 / Windows 通知区域）：打开页面、设置、历史、清除历史（二次确认）、退出。
- **「历史」子菜单**：最近 **30 条**，点条目即复制回剪贴板并模拟粘贴；图片同样可用。
- **全局快捷键**（服务本机生效）：

  | 作用 | macOS 默认 | Windows / Linux 默认 |
  | --- | --- | --- |
  | 打开页面并弹二维码 | `⌘ ⇧ Q` | `Ctrl + Shift + Q` |
  | 弹出最近 30 条历史菜单 | `⌘ ⇧ V` | `Ctrl + Shift + V` |

  可在设置里改；组合键被系统占用时会重注册失败并拒绝保存（不会写坏配置）。
- **启动通知**：就绪后弹「LanCoClip已就绪 · 点击打开剪贴板记录页面」，点击横幅直接开页面。
- **单实例**：程序已经在跑时再次双击，不会起第二个实例，而是由已在运行的那个弹「LanCoClip已在运行 · 点击即可打开剪贴板记录页面」。
- **开机自启**：macOS 写 LaunchAgent plist（`launchd` 登录时拉起），Windows 写 `HKCU\...\Run`；Linux 不支持（页面不展示开关）。

## 快速开始

### 命令行

```bash
go build -o lanoclip .
./lanoclip                   # 默认端口 6116
./lanoclip -port 8080        # 指定端口
./lanoclip -interval 300ms   # 轮询间隔（仅无变更事件的平台生效）
./lanoclip -tray             # macOS 菜单栏模式（.app 内自动开启）
./lanoclip -data /tmp/lcp    # 隔离数据目录（自测务必加，别污染真实记录）
```

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-port` | `6116` | HTTP 端口；被占用时自动顺延（最多试 10 个） |
| `-interval` | `500ms` | 轮询间隔，仅 Linux 等无原生变更事件的平台用到 |
| `-tray` | `false` | 菜单栏模式；macOS `.app` 内自动开启 |
| `-data` | 用户配置目录 | 数据目录；也可用环境变量 `LANOCLIP_DIR`（优先级低于 `-data`） |

启动后终端会打印手机可访问的地址（如 `http://192.168.x.x:6116`），扫码或输入即可查看；在本机打开同一地址可以清空和修改设置。

### 打包

```bash
./build-mac.sh 1.2.4        # 不传版本默认 1.2.2
./build-windows.sh 1.2.4    # 同上
```

产物在 `dist/`：

| 产物 | 说明 |
| --- | --- |
| `LanCoClip.app` | macOS 本机架构，双击即用（已用固定身份 `LanCoClip Dev` 签名，辅助功能授权跨构建有效） |
| `LanCoClip-<version>-mac-arm64.zip` | Apple Silicon |
| `LanCoClip-<version>-mac-x64.zip` | Intel |
| `LanCoClip-<version>-windows-x64.zip` | Intel / AMD 64 位 |
| `LanCoClip-<version>-windows-arm64.zip` | Windows on ARM |

- 首次运行 `.app` 若被 Gatekeeper 拦截：`xattr -dr com.apple.quarantine dist/LanCoClip.app`
- Windows exe 用 `-H windowsgui` 编译，不弹黑色控制台窗口，日志写到数据目录的 `app.log`；首次运行若被 SmartScreen 拦截，选「更多信息 → 仍要运行」
- 图标缺失时脚本会自动 `go run ./tools/genicons` 生成（`-from <源图>` 可从自定义图片派生）
- exe 的图标 / 版本信息由 `go-winres` 生成；没装会跳过并提示，功能不受影响：`go install github.com/tc-hib/go-winres@latest`
- 构建要求：Go 1.26.10+；macOS 包必须在本机编译（菜单栏走 cgo / Cocoa，需 Xcode 命令行工具），Windows 端是纯 Go，可 `CGO_ENABLED=0` 交叉编译

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | H5 页面（`no-store`，不缓存） |
| GET | `/api/records?type=text&page=1&size=10` | 分页记录，`type`=text\|image（默认 text）；返回 `records` / `type` / `page` / `size` / `total` / `totalPages` / `hasMore` / `counts` / `settings` / `canClear` / `shareUrl` / `version`（`size` 默认 10，上限 500） |
| GET | `/api/recent?limit=30` | 最近记录（文本 + 图片合并，图片只带元数据），供历史弹层使用 |
| GET | `/api/image?id=<id>` | 图片记录的 PNG 原图 |
| POST | `/api/clear?type=text` | 清空；`type` 指定只清一类，不带则全清（仅服务本机、非手机，否则 403） |
| POST | `/api/suppress` | 上报「这段内容是从页面复制的」，本次变更跳过入库；`{"kind":"text","content":"…"}` 或 `{"kind":"image","id":123}`；仅服务本机 |
| GET | `/api/qr?text=<url>` | 生成 URL 二维码 PNG |
| GET | `/api/settings` | 读取配置：`maxText` / `maxImage` / `autostart` / `autostartSupported` / `hotkeyQR` / `hotkeyHistory` |
| POST | `/api/settings` | 修改配置（保留条数、开机自启、快捷键可分开提交）；仅服务本机、非手机，否则 403；保留条数保存后立即生效并淘汰最旧记录，返回 `trimmedText` / `trimmedImage` |
| POST | `/api/activate` | 单实例探测：已运行的实例回 `{"ok":true,"app":"lanoclip","url":"http://127.0.0.1:<port>"}` 并弹「已在运行」通知；新的一次启动探测到它就退出。仅服务本机，非本机 403 |

## 数据存储

目录优先级：`-data` > 环境变量 `LANOCLIP_DIR` > 各平台默认用户配置目录下的 `LanCoClip/`：

| 平台 | 数据目录 |
| --- | --- |
| macOS | `~/Library/Application Support/LanCoClip/` |
| Windows | `%APPDATA%\LanCoClip\`（`C:\Users\<用户名>\AppData\Roaming\LanCoClip\`） |
| Linux | `~/.config/LanCoClip/`（设了 `XDG_CONFIG_HOME` 时为 `$XDG_CONFIG_HOME/LanCoClip/`） |

目录内容：

| 文件 | 内容 |
| --- | --- |
| `texts.json` | 文本记录 |
| `images.json` | 图片记录元数据 |
| `images/` | 图片原图 PNG（文件名即记录 id） |
| `settings.json` | 保留条数、快捷键等配置 |
| `app.log` | 运行日志（托盘模式没有终端，排查问题看这里） |

- 旧版本的单文件 `history.json` 会在首次启动时**自动拆分迁移**，原文件改名 `history.json.migrated` 保留，不丢数据。
- 备份 / 迁移：把整个目录拷走即可（`images/` 与两个 json 一起拷）。

## 说明

- Linux 需要 `xclip` 或 `xsel`；Windows 用 PowerShell `Get-Clipboard` 作兜底。图片读写支持 macOS（Cocoa / `NSPasteboard`）和 Windows（Win32 `CF_DIB`，直接调 API 不起子进程），Linux 暂不支持图片。
- 自测 / 调试请一定用 `-data` 指定隔离目录，别把测试文本写进真实记录。
- 页面行为可用 Playwright 做真实设备模拟自测（iPhone UA、iPhone + 桌面版 UA、桌面三种场景）。
- 仓库的 `.gitignore` 屏蔽了 `*_test.go`，测试文件只留在本地，不随仓库分发；本地仍可 `go test ./...`。
