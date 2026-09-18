#!/usr/bin/env bash
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
if [ -z "$SDK_ROOT" ]; then
    echo "ANDROID_SDK_ROOT / ANDROID_HOME is not set" >&2
    exit 1
fi

NDK_ROOT="${ANDROID_NDK_HOME:-$SDK_ROOT/ndk/27.0.12077973}"
if [ ! -d "$NDK_ROOT" ]; then
    echo "Android NDK not found: $NDK_ROOT" >&2
    exit 1
fi

API=26
TOOLCHAIN="$NDK_ROOT/toolchains/llvm/prebuilt/linux-x86_64/bin"
CC="$TOOLCHAIN/aarch64-linux-android${API}-clang"

if [ ! -x "$CC" ]; then
    echo "Android clang not found: $CC" >&2
    exit 1
fi

OUT_DIR="$ROOT/android/app/src/main/jniLibs/arm64-v8a"
OUT="$OUT_DIR/libopenflux_native.so"

mkdir -p "$OUT_DIR"

echo "==> Building OpenFlux native Android ARM64"

(
    cd "$ROOT"

    export GOOS=android
    export GOARCH=arm64
    export CGO_ENABLED=1
    export CC="$CC"

    go build \
        -trimpath \
        -ldflags="-s -w -checklinkname=0 -extldflags=-Wl,-z,max-page-size=16384" \
        -o "$OUT" \
        .
)

file "$OUT"
ls -lh "$OUT"
sha256sum "$OUT"

echo "OK: $OUT"
