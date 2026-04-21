#!/bin/bash
#
# ShanLian SDK Android NDK 交叉编译脚本
#

set -e

# 配置
NDK_PATH=${ANDROID_NDK_HOME:-"$HOME/Android/Sdk/ndk/25.2.5"}
MIN_SDK=21
SDK_VERSION="0.1.0"
OUTPUT_DIR="dist/android"

# 创建输出目录
mkdir -p $OUTPUT_DIR

# 编译配置
CONFIGURATIONS=(
    "aarch64:arm64-v8a:25.2.5"
    "armv7:armeabi-v7a:25.2.5"
    "x86_64:x86_64:25.2.5"
    "i686:i686:25.2.5"
)

echo "======================================"
echo "ShanLian SDK Android NDK Cross Compile"
echo "======================================"
echo "NDK Path: $NDK_PATH"
echo "Output: $OUTPUT_DIR"
echo ""

for config in "${CONFIGURATIONS[@]}"; do
    IFS=':' read -r arch abi toolchain <<< "$config"

    echo "--------------------------------------"
    echo "Building for $abi ($arch)"
    echo "--------------------------------------"

    # 工具链
    case $arch in
        aarch64)
            TOOLCHAIN="$NDK_PATH/toolchains/llvm/prebuilt/linux-x86_64"
            export CC="$TOOLCHAIN/bin/aarch64-linux-android21-clang"
            export CXX="$TOOLCHAIN/bin/aarch64-linux-android21-clang++"
            ;;
        armv7)
            TOOLCHAIN="$NDK_PATH/toolchains/llvm/prebuilt/linux-x86_64"
            export CC="$TOOLCHAIN/bin/armv7a-linux-androideabi21-clang"
            export CXX="$TOOLCHAIN/bin/armv7a-linux-androideabi21-clang++"
            ;;
        x86_64)
            TOOLCHAIN="$NDK_PATH/toolchains/llvm/prebuilt/linux-x86_64"
            export CC="$TOOLCHAIN/bin/x86_64-linux-android21-clang"
            export CXX="$TOOLCHAIN/bin/x86_64-linux-android21-clang++"
            ;;
        i686)
            TOOLCHAIN="$NDK_PATH/toolchains/llvm/prebuilt/linux-x86_64"
            export CC="$TOOLCHAIN/bin/i686-linux-android21-clang"
            export CXX="$TOOLCHAIN/bin/i686-linux-android21-clang++"
            ;;
    esac

    # 创建 build 目录
    BUILD_DIR="build/android_$abi"
    mkdir -p $BUILD_DIR
    cd $BUILD_DIR

    # CMake 配置
    cmake .. \
        -DCMAKE_TOOLCHAIN_FILE=$NDK_PATH/build/cmake/android.toolchain.cmake \
        -DANDROID_ABI=$abi \
        -DANDROID_PLATFORM=android-$MIN_SDK \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=$OUTPUT_DIR/$abi

    # 编译
    cmake --build . --config Release

    cd ..
done

echo ""
echo "======================================"
echo "Build completed!"
echo "Output: $OUTPUT_DIR"
echo "======================================"
