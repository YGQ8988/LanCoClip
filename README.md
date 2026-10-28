<div align="center">

<img src="web/appicon.png" alt="LanCoClip" width="120" />

# LanCoClip

**局域网剪贴板历史工具**：后台常驻，自动留存电脑复制的文本与截图，局域网内随时查看、一键取回剪贴板，其他设备也能反向推送到电脑。

</div>

## 功能特性

### 自动记录，无需手动保存

- 电脑上每一次复制都会自动记录：**文本**和**截图 / 图片**都能存，一条不漏，程序异常退出也不会丢失刚复制的内容。
- 文本与图片**分开存储、分开查看**，互不干扰。
- **自动去重**：同一内容反复复制只保留一条，不堆积。
- 记录数量可设置上限（文本 10–5000 条、图片 5–1000 条，默认 500 / 200），超出自动清理最旧的记录。
- 从页面上复制的内容不会重复入库，避免「自产自销」的冗余记录。

### 局域网随时查看与取回

- 手机 / 平板连同一 Wi-Fi，浏览器打开页面即可看到电脑的剪贴板历史。
- 文本一键**复制**回剪贴板；图片一键**复制**或**保存 PNG 原图**。
- PC 分页浏览（每页 32 条）；手机端上滑加载更多。
- 新复制的内容实时出现；断连会提示并自动重试。

### 其他电脑/手机反向推送

- 其他电脑/手机上也能把内容推给电脑：**发文本**、**传图片**（相册 / 文件管理器选图，自动转 PNG 入库）。
- 悬浮推送按钮可**拖动**到任意位置，位置自动记住。

### 扫码即连

- 页面顶部显示局域网访问地址，一键生成**二维码**，手机扫码即开；也可一键复制访问链接。

### 桌面快捷操作（macOS / Windows）

- **菜单栏 / 托盘菜单**：打开页面、设置、最近历史、清除历史、退出。
- **最近历史**：最近 30 条一键取回，点击即复制回剪贴板。
- **全局快捷键**（可自定义，默认组合如下，可在设置中修改）：

  | 作用 | macOS 默认 | Windows 默认 |
  | --- | --- | --- |
  | 打开页面并弹二维码 | `⌘ ⇧ Q` | `Ctrl + Shift + Q` |
  | 弹出最近 30 条历史菜单 | `⌘ ⇧ V` | `Ctrl + Shift + V` |

- **开机自启**：登录后自动在后台运行，启动即弹通知，点击横幅直接打开页面；重复启动不会开第二个实例。

### 个性化设置

- **主题**：默认跟随系统，也可手动切换亮 / 暗色并记住。
- **保留条数**：分别设置文本、图片的记录上限，保存立即生效。
- **快捷键**：点按捕获自定义组合键，冲突时自动拒绝保存。

### 支持平台

- **macOS / Windows**：完整支持（托盘、通知、开机自启、全局快捷键、图片记录）。

## 界面预览

### PC 端

**亮色主题**

| 主页 · 文本 | 主页 · 图片 |
| --- | --- |
| <img src="screenshots/亮色主页-PC-文本.png" width="540" alt="PC 主页-文本（亮色）"/> | <img src="screenshots/亮色主页-PC-图片.png" width="540" alt="PC 主页-图片（亮色）"/> |

| 添加文本 | 二维码 |
| --- | --- |
| <img src="screenshots/亮色添加文本-PC.png" width="540" alt="PC 添加文本（亮色）"/> | <img src="screenshots/亮色二维码-PC.png" width="540" alt="PC 二维码（亮色）"/> |

| 设置 · 通用 | 设置 · 快捷键 |
| --- | --- |
| <img src="screenshots/亮色设置-PC-通用.png" width="540" alt="PC 设置-通用（亮色）"/> | <img src="screenshots/亮色设置-PC-快捷键.png" width="540" alt="PC 设置-快捷键（亮色）"/> |

**暗色主题**

| 主页 · 文本 | 主页 · 图片 |
| --- | --- |
| <img src="screenshots/暗色主页-PC-文本.png" width="540" alt="PC 主页-文本（暗色）"/> | <img src="screenshots/暗色主页-PC-图片.png" width="540" alt="PC 主页-图片（暗色）"/> |

| 添加文本 | 二维码 |
| --- | --- |
| <img src="screenshots/暗色添加文本-PC.png" width="540" alt="PC 添加文本（暗色）"/> | <img src="screenshots/暗色二维码-PC.png" width="540" alt="PC 二维码（暗色）"/> |

| 设置 · 通用 | 设置 · 快捷键 |
| --- | --- |
| <img src="screenshots/暗色设置-PC-通用.png" width="540" alt="PC 设置-通用（暗色）"/> | <img src="screenshots/暗色设置-PC-快捷键.png" width="540" alt="PC 设置-快捷键（暗色）"/> |

**托盘菜单**（macOS 菜单栏 / Windows 通知区域，与主题无关）

| 托盘菜单 |
| --- |
| <img src="screenshots/托盘菜单-PC.png" width="540" alt="托盘菜单"/> |

### H5 端（手机）

**亮色主题**

| 主页 · 文本 | 主页 · 图片 | 添加文本 | 二维码 |
| --- | --- | --- | --- |
| <img src="screenshots/亮色主页-H5-文本.jpg" width="220" alt="H5 主页-文本（亮色）"/> | <img src="screenshots/亮色主页-H5-图片.jpg" width="220" alt="H5 主页-图片（亮色）"/> | <img src="screenshots/亮色添加文本-H5.jpg" width="220" alt="H5 添加文本（亮色）"/> | <img src="screenshots/亮色二维码-H5.jpg" width="220" alt="H5 二维码（亮色）"/> |

**暗色主题**

| 主页 · 文本 | 主页 · 图片 | 添加文本 | 二维码 |
| --- | --- | --- | --- |
| <img src="screenshots/暗色主页-H5-文本.jpg" width="220" alt="H5 主页-文本（暗色）"/> | <img src="screenshots/暗色主页-H5-图片.jpg" width="220" alt="H5 主页-图片（暗色）"/> | <img src="screenshots/暗色添加文本-H5.jpg" width="220" alt="H5 添加文本（暗色）"/> | <img src="screenshots/暗色二维码-H5.jpg" width="220" alt="H5 二维码（暗色）"/> |

## 快速开始

```bash
go build -o lanoclip .
./lanoclip                   # 默认端口 6116
./lanoclip -port 8080        # 自定义端口
./lanoclip -data /tmp/lcp    # 自定义数据目录
```

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-port` | `6116` | HTTP 端口；被占用时自动顺延（最多试 10 个） |
| `-tray` | `false` | 菜单栏模式；macOS `.app` 内自动开启 |
| `-data` | 用户配置目录 | 数据目录；也可用环境变量 `LANOCLIP_DIR`（优先级低于 `-data`） |

启动后终端会打印手机可访问的地址（如 `http://192.168.x.x:6116`），扫码或输入即可查看；在本机打开同一地址可以清空和修改设置（手机端不可）。

macOS / Windows 打包：`./build-mac.sh <版本>`、`./build-windows.sh <版本>`，产物在 `dist/`（macOS 为 `.app` 与 zip，Windows 为 zip）。

## 数据存储

目录优先级：`-data` > 环境变量 `LANOCLIP_DIR` > 各平台默认用户配置目录下的 `LanCoClip/`：

| 平台 | 数据目录 |
| --- | --- |
| macOS | `~/Library/Application Support/LanCoClip/` |
| Windows | `%APPDATA%\LanCoClip\`（`C:\Users\<用户名>\AppData\Roaming\LanCoClip\`） |

目录内容：

| 文件 | 内容 |
| --- | --- |
| `texts.json` | 文本记录 |
| `images.json` | 图片记录元数据 |
| `images/` | 图片原图 PNG（文件名即记录 id） |
| `settings.json` | 保留条数、快捷键等配置 |
| `app.log` | 运行日志（托盘模式没有终端，排查问题看这里） |

- 备份 / 迁移：把整个目录拷走即可（`images/` 与两个 json 一起拷）。

## 说明

- 自测 / 调试请一定用 `-data` 指定隔离目录，别把测试文本写进真实记录。
