#!/bin/bash
# 两个构建脚本共用的命名 / 版本配置（由 build-mac.sh、build-windows.sh source）。
# 单独改这里即可保持 macOS 包与 Windows exe 的版本号、构建时间、显示名一致。
#
# 用法：source ./build-config.sh "$1"
# 也可通过环境变量 LANOCLIP_VERSION 指定版本（优先级低于命令行参数）。

APP_NAME="LanCoClip"
BUNDLE_ID="com.tuhu.lanoclip"
DISPLAY_NAME="剪贴板"

VERSION="${1:-${LANOCLIP_VERSION:-1.2.2}}"
# 构建时间（本机时区），经 -ldflags 注入 lanoclip/internal/server.BuildTime，
# 与版本号一起显示在设置弹窗里。
BUILD_TIME="$(date '+%Y-%m-%d %H:%M')"

# 自签名证书的 p12 口令。仅用于本机的固定签名身份，不是分发的私钥口令；
# 改这个值后需要删掉 codesign/lanoclip.p12 让脚本重新生成一份。
LANOCLIP_SIGN_PASSWORD="${LANOCLIP_SIGN_PASSWORD:-lanoclip-sign}"
