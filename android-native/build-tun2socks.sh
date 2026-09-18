#!/usr/bin/env bash
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
JNI="$ROOT/android/app/src/main/jni"
OUT="$ROOT/android/app/src/main/jniLibs"

SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
NDK_ROOT="${ANDROID_NDK_HOME:-$SDK_ROOT/ndk/27.0.12077973}"

if [ ! -d "$NDK_ROOT" ]; then
    echo "Android NDK not found: $NDK_ROOT" >&2
    exit 1
fi

NDK_BUILD="$NDK_ROOT/ndk-build"

if [ ! -x "$NDK_BUILD" ]; then
    echo "ndk-build not found: $NDK_BUILD" >&2
    exit 1
fi

cd "$JNI"

"$NDK_BUILD" \
    NDK_PROJECT_PATH=. \
    NDK_APPLICATION_MK=Application.mk \
    APP_BUILD_SCRIPT=Android.mk \
    NDK_LIBS_OUT="$JNI/libs" \
    NDK_OUT="$JNI/obj"

for abi in armeabi-v7a arm64-v8a x86 x86_64; do
    mkdir -p "$OUT/$abi"

    test -f "$JNI/libs/$abi/tun2socks"
    test -f "$JNI/libs/$abi/pdnsd"
    test -f "$JNI/libs/$abi/libsystem.so"

    cp "$JNI/libs/$abi/tun2socks" \
       "$OUT/$abi/libtun2socks.so"

    cp "$JNI/libs/$abi/pdnsd" \
       "$OUT/$abi/libpdnsd.so"

    cp "$JNI/libs/$abi/libsystem.so" \
       "$OUT/$abi/libsystem.so"
done

echo
echo "Native VPN binaries:"
find "$OUT" -type f \
    \( -name 'libtun2socks.so' \
       -o -name 'libpdnsd.so' \
       -o -name 'libsystem.so' \) \
    -print | sort
