#!/bin/bash
# LanCoClip Windows 构建脚本（可在 macOS / Linux 上运行，Windows 上需有 zip 或 python3）
#
# 产物（zip 名体现架构，解压后统一是 LanCoClip.exe）：
#   dist/LanCoClip-<version>-windows-x64.zip    （Intel / AMD 64 位）
#   dist/LanCoClip-<version>-windows-arm64.zip  （Windows on ARM）
#
# 说明：
#   * 托盘用 getlantern/systray，Windows 端是纯 Go 实现，因此可交叉编译（CGO_ENABLED=0）。
#   * -H windowsgui：GUI 子系统，双击不弹黑窗口；日志写入数据目录的 app.log。
#   * 运行后常驻任务栏（通知区域），右键菜单：「打开页面」「退出」。
# 用法：./build-windows.sh [version]
set -e
cd "$(dirname "$0")"
export COPYFILE_DISABLE=1
mkdir -p dist .build winres

APP_NAME="LanCoClip"
DISPLAY_NAME="剪贴板"
VERSION="${1:-1.2.2}"
# 构建时间（本机时区，即北京时间），注入 main.buildTime，设置弹窗里随版本号一起展示
BUILD_TIME="$(date '+%Y-%m-%d %H:%M')"
echo "构建版本：${VERSION}（${BUILD_TIME}）"
echo "Go 工具链：$(go version)（go.mod 要求 1.26.10+，含 CVE-2026-111943 修复）"

# ── 1. 图标资源（主图标 + Windows 托盘 .ico + exe .ico）────────
if [ ! -f internal/tray/menuicon.png ] \
   || [ ! -f internal/tray/menuicon_win.ico ] \
   || [ ! -f winres/lanoclip.ico ]; then
  echo ">> 生成图标资源（tools/genicons）"
  go run ./tools/genicons
fi

# ── 2. exe 的图标/版本资源（go-winres → rsrc_windows_<arch>.syso）──
# .syso 必须落在项目根目录，go build 才会按命名约定自动链接（放子目录图标会丢）。
# 找不到工具时跳过，exe 用系统默认图标，不影响功能。
gen_winres() {
  local gw=""
  if command -v go-winres >/dev/null 2>&1; then
    gw="go-winres"
  elif [ -x "$(go env GOPATH)/bin/go-winres" ]; then
    gw="$(go env GOPATH)/bin/go-winres"
  fi
  if [ -z "$gw" ]; then
    echo "  [提示] 未找到 go-winres，跳过 exe 图标/版本资源（使用系统默认图标）"
    echo "        如需图标：go install github.com/tc-hib/go-winres@latest"
    return
  fi
  if [ ! -f winres/lanoclip.ico ]; then
    echo "  [提示] 未找到 winres/lanoclip.ico，跳过 exe 图标资源"
    return
  fi
  echo "  [winres] 生成 Windows 图标/版本资源（v${VERSION}）"
  "$gw" simply --arch amd64,arm64 --icon winres/lanoclip.ico \
    --out rsrc --manifest gui \
    --product-name "${APP_NAME}" --file-description "${APP_NAME}" \
    --product-version "${VERSION}" --file-version "${VERSION}" \
    --original-filename "${APP_NAME}.exe" --copyright "${DISPLAY_NAME}"
}

# 打包成 zip：优先 zip（-j 只存文件名、-X 不存 macOS 扩展属性），否则回退 python3
make_zip() {
  local zip_name="$1" file="$2"
  rm -f "${zip_name}"
  if command -v zip >/dev/null 2>&1; then
    zip -q -j -X "${zip_name}" "${file}"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "import sys,zipfile,os; z=zipfile.ZipFile(sys.argv[1],'w',zipfile.ZIP_DEFLATED); z.write(sys.argv[2], os.path.basename(sys.argv[2])); z.close()" "${zip_name}" "${file}"
  else
    echo "  [错误] 需要 zip 或 python3 才能打包" >&2
    exit 1
  fi
}

arch_label() {
  case "$1" in
    amd64) echo "x64" ;;
    386)   echo "x86" ;;
    *)     echo "$1" ;;
  esac
}

# build_exe <goarch>
build_exe() {
  local goarch="$1"
  local label="$(arch_label "$goarch")"
  local stage=".build/windows-${label}"
  local zip_name="dist/${APP_NAME}-${VERSION}-windows-${label}.zip"

  echo ""
  echo ">> 编译 windows/${goarch}（→ ${zip_name}）"
  rm -rf "${stage}"
  mkdir -p "${stage}"
  # 解压后统一是 LanCoClip.exe，架构只体现在 zip 文件名上
  GOOS=windows GOARCH="${goarch}" CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w -H windowsgui -X 'main.version=${VERSION}' -X 'main.buildTime=${BUILD_TIME}'" \
    -o "${stage}/${APP_NAME}.exe" .

  make_zip "${zip_name}" "${stage}/${APP_NAME}.exe"
  printf "    %s  %s\n" "$(du -h "${zip_name}" | awk '{print $1}')" "$(basename "${zip_name}")"
  rm -rf "${stage}"
}

gen_winres

build_exe amd64
build_exe arm64

echo ""
echo "完成，产物在 dist/："
ls -lh dist/*windows*.zip 2>/dev/null | awk '{printf "  %-8s %s\n", $5, $NF}'
echo ""
echo "用法：解压 zip 得到 ${APP_NAME}.exe，双击运行 —— 任务栏（通知区域）出现图标，右键可『打开页面』或『退出』。"
echo "首次运行若被 SmartScreen 拦截，选择「更多信息 → 仍要运行」（未签名程序的常规提示）。"
