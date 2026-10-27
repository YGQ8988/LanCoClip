#!/bin/bash
# LanCoClip macOS 构建脚本（必须在 Mac 上运行）
#
# 产物：
#   dist/LanCoClip.app                      本机架构的 .app（双击即用）
#   dist/LanCoClip-<version>-mac-arm64.zip  Apple Silicon
#   dist/LanCoClip-<version>-mac-x64.zip    Intel
#
# 前置：1) 安装 Go   2) 安装 Xcode 命令行工具: xcode-select --install
# 说明：菜单栏图标用 systray，在 macOS 走 cgo(Cocoa)，无法交叉编译，必须在 Mac 本机编译。
# 用法：./build-mac.sh [version]（不传则默认 1.0.0）
set -e
cd "$(dirname "$0")"
# 禁用 macOS cp 的 AppleDouble（._xxx）副文件，避免 .app 内混入垃圾
export COPYFILE_DISABLE=1
mkdir -p dist .build assets

APP_NAME="LanCoClip"
BUNDLE_ID="com.tuhu.lanoclip"
VERSION="${1:-1.2.2}"
# 构建时间（本机时区，即北京时间），注入 main.buildTime，设置弹窗里随版本号一起展示
BUILD_TIME="$(date '+%Y-%m-%d %H:%M')"
# 稳定的自签名身份：辅助功能等 TCC 授权绑定在证书上，重新构建不再要求重新授权。
# （ad-hoc 签名每次构建 CDHash 都变，macOS 会当成"新程序"要求重新授权）
SIGN_IDENTITY="LanCoClip Dev"
echo "构建版本：${VERSION}（${BUILD_TIME}）"
echo "Go 工具链：$(go version)（go.mod 要求 1.26.10+，含 CVE-2026-111943 修复）"

# cgo 编译目标固定 macOS 11.0
export MACOSX_DEPLOYMENT_TARGET=11.0
export CGO_CFLAGS="-mmacosx-version-min=11.0"

# ── 1. 图标资源 ───────────────────────────────────────────────
# menuicon.png 会经 //go:embed 编进二进制，favicon.png 也要一起打包，缺失则先生成。
if [ ! -f internal/tray/menuicon.png ] || [ ! -f assets/appicon.png ] || [ ! -f web/favicon.png ]; then
  echo ">> 生成图标资源（tools/genicons）"
  go run ./tools/genicons
fi

# 由 1024 的 appicon.png 生成 AppIcon.icns（iconutil 要求固定的文件名集合）
if [ ! -f assets/AppIcon.icns ] || [ assets/appicon.png -nt assets/AppIcon.icns ]; then
  echo ">> 生成 assets/AppIcon.icns"
  ICONSET="assets/AppIcon.iconset"
  rm -rf "${ICONSET}"
  mkdir -p "${ICONSET}"
  gen_icon() { sips -z "$1" "$1" assets/appicon.png --out "${ICONSET}/$2" >/dev/null; }
  gen_icon 16   icon_16x16.png
  gen_icon 32   icon_16x16@2x.png
  gen_icon 32   icon_32x32.png
  gen_icon 64   icon_32x32@2x.png
  gen_icon 128  icon_128x128.png
  gen_icon 256  icon_128x128@2x.png
  gen_icon 256  icon_256x256.png
  gen_icon 512  icon_256x256@2x.png
  gen_icon 512  icon_512x512.png
  gen_icon 1024 icon_512x512@2x.png
  iconutil -c icns "${ICONSET}" -o assets/AppIcon.icns
  rm -rf "${ICONSET}"
fi

# ── 2. 签名证书 ───────────────────────────────────────────────
# ensure_signing_cert：确保 codesign 可用名为 ${SIGN_IDENTITY} 的自签名证书。
# 证书与私钥存于 codesign/（已 gitignore，含私钥勿入库）；导入 login 钥匙串
# 时用 -T /usr/bin/codesign 授权 codesign 免交互访问。失败则回退 ad-hoc。
ensure_signing_cert() {
  if security find-identity -v -p codesigning 2>/dev/null | grep -q "${SIGN_IDENTITY}"; then
    echo ">> 签名身份 ${SIGN_IDENTITY} 已存在"
    return 0
  fi
  echo ">> 首次运行：生成自签名代码签名证书 ${SIGN_IDENTITY}"
  local dir="codesign"
  mkdir -p "${dir}"
  # 证书文件按身份名存放（lanoclip.p12），与旧证书文件名区分
  if [ ! -f "${dir}/lanoclip.p12" ]; then
    openssl req -x509 -newkey rsa:2048 -keyout "${dir}/lanoclip.key.pem" -out "${dir}/lanoclip.cert.pem" \
      -days 3650 -nodes -subj "/CN=${SIGN_IDENTITY}/O=LanCoClip/C=CN" \
      -addext "keyUsage=digitalSignature" -addext "extendedKeyUsage=codeSigning" \
      || return 1
    # macOS security 只认传统 PKCS#12 加密，必须 -legacy（或 SHA1/3DES 参数）
    openssl pkcs12 -export -legacy -out "${dir}/lanoclip.p12" \
      -inkey "${dir}/lanoclip.key.pem" -in "${dir}/lanoclip.cert.pem" -passout pass:lanoclip-sign \
      || openssl pkcs12 -export -out "${dir}/lanoclip.p12" -inkey "${dir}/lanoclip.key.pem" \
      -in "${dir}/lanoclip.cert.pem" -passout pass:lanoclip-sign \
      -keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1 \
      || return 1
  fi
  security import "${dir}/lanoclip.p12" -k ~/Library/Keychains/login.keychain-db \
    -P lanoclip-sign -T /usr/bin/codesign || return 1
  security add-trusted-cert -r trustRoot \
    -k ~/Library/Keychains/login.keychain-db "${dir}/lanoclip.cert.pem" || return 1
  security find-identity -v -p codesigning 2>/dev/null | grep -q "${SIGN_IDENTITY}" || {
    echo "!! 证书导入后仍不可用"; return 1
  }
  echo ">> 证书已导入并信任（有效期 10 年）"
  return 0
}

# sign_app <app_dir>：优先固定身份签名（授权跨构建保持），失败回退 ad-hoc。
sign_app() {
  if ensure_signing_cert; then
    if codesign --force --deep --sign "${SIGN_IDENTITY}" --options runtime "$1" 2>/dev/null; then
      echo "    已用固定身份 ${SIGN_IDENTITY} 签名（授权持续有效）"
      return 0
    fi
    echo "    !! 固定身份签名失败，回退 ad-hoc（每次重新打包需重新授权辅助功能）"
  else
    echo "    !! 证书生成失败，回退 ad-hoc（每次重新打包需重新授权辅助功能）"
  fi
  codesign --force --deep --sign - --options runtime "$1"
}

# ── 3. 分架构构建 ─────────────────────────────────────────────
# 本机架构：arm64 / x86_64
HOST_ARCH="$(uname -m)"

# arch_label：把 goarch / 本机架构（uname -m）映射为文件名标识（arm64 / x64）
arch_label() {
  case "$1" in
    amd64|x86_64) echo "x64" ;;
    *)            echo "$1" ;;
  esac
}

# build_app <goarch> <keep>：编译、打包、签名、压缩单个架构的 .app。
# keep=true 时把 .app 留在 dist/ 供直接双击（只对最后一个调用生效）。
build_app() {
  local goarch="$1" keep="$2"
  local label="$(arch_label "$goarch")"
  local app_dir=".build/${APP_NAME}.app"
  local zip_name="dist/${APP_NAME}-${VERSION}-mac-${label}.zip"

  echo ""
  echo ">> 编译 ${goarch}（本机 ${HOST_ARCH} → ${zip_name}）"
  GOOS=darwin GOARCH="${goarch}" CGO_ENABLED=1 \
    go build -trimpath -ldflags="-s -w -X 'main.version=${VERSION}' -X 'main.buildTime=${BUILD_TIME}'" -o ".build/${APP_NAME}-${goarch}" .

  rm -rf "${app_dir}"
  mkdir -p "${app_dir}/Contents/MacOS" "${app_dir}/Contents/Resources"

  # 注意：这里用不带引号的 heredoc，以便把 ${VERSION} 展开进 plist
  cat > "${app_dir}/Contents/Info.plist" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>剪贴板</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon.icns</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <!-- true = 菜单栏常驻（agent app），Dock 不显示图标；右键菜单可打开页面 / 退出 -->
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
PLIST

  # 可执行文件名必须与 Info.plist 的 CFBundleExecutable 完全一致（含大小写）
  cp ".build/${APP_NAME}-${goarch}" "${app_dir}/Contents/MacOS/${APP_NAME}"
  chmod +x "${app_dir}/Contents/MacOS/${APP_NAME}"

  cp assets/AppIcon.icns "${app_dir}/Contents/Resources/AppIcon.icns"
  touch "${app_dir}"
  find "${app_dir}" -name '._*' -delete 2>/dev/null || true

  # 必须签名：macOS 26 (Tahoe) 会忽略未签名的 app，菜单栏权限里根本不出现本程序。
  # 用固定自签名身份，辅助功能授权跨构建保持（详见 sign_app 注释）。
  sign_app "${app_dir}"
  echo "    ${app_dir} 已生成并签名"

  rm -f "${zip_name}"
  ditto -c -k --sequesterRsrc --keepParent "${app_dir}" "${zip_name}"
  echo "    ${zip_name} 已生成"

  if [ "${keep}" = "true" ]; then
    rm -rf "dist/${APP_NAME}.app"
    mv "${app_dir}" "dist/${APP_NAME}.app"
  else
    rm -rf "${app_dir}"
  fi
}

# 先构建非本机架构，最后构建本机架构并把 .app 留在 dist/ 方便双击
if [ "${HOST_ARCH}" = "arm64" ]; then
  build_app amd64 false
  build_app arm64 true
else
  build_app arm64 false
  build_app amd64 true
fi

rm -f .build/${APP_NAME}-arm64 .build/${APP_NAME}-amd64
echo "    已清理 .build/ 中间产物"

echo ""
echo "完成，产物在 dist/："
if [ -d "dist/${APP_NAME}.app" ]; then
  printf "  %-8s %s\n" "$(du -sh "dist/${APP_NAME}.app" | awk '{print $1}')" "dist/${APP_NAME}.app"
fi
ls -lh dist/*.zip 2>/dev/null | awk '{printf "  %-8s %s\n", $5, $NF}'
echo ""
echo "用法：双击 dist/${APP_NAME}.app —— 顶部菜单栏出现图标，点击（右键）可『打开页面』或『退出』。"
echo "首次运行若被 Gatekeeper 拦截，先移除 quarantine："
echo "  xattr -dr com.apple.quarantine dist/${APP_NAME}.app"
