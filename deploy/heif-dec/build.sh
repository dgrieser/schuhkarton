#!/bin/sh
# Builds a static heif-dec for one target: build.sh <goarch> <cpu> <prefix of
# the cross compiler>. Sources in /src (see Dockerfile), result in
# /out/linux_<goarch>.gz.
set -eu
arch=$1 cpu=$2 cross=$3
work=/work/$arch
prefix=$work/prefix
mkdir -p "$work" /out
cd "$work"

cat >toolchain.cmake <<EOF
set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR $cpu)
set(CMAKE_C_COMPILER ${cross}gcc)
set(CMAKE_CXX_COMPILER ${cross}g++)
set(CMAKE_FIND_ROOT_PATH $prefix)
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)
EOF
# only what is built here, nothing of the build machine
export PKG_CONFIG_LIBDIR=$prefix/lib/pkgconfig PKG_CONFIG_PATH=

cmake_build() {
    src=$1 dir=$2
    shift 2
    cmake -S "$src" -B "$dir" \
        -DCMAKE_TOOLCHAIN_FILE="$work/toolchain.cmake" \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX="$prefix" \
        -DCMAKE_INSTALL_LIBDIR=lib \
        -DCMAKE_PREFIX_PATH="$prefix" \
        -DBUILD_SHARED_LIBS=OFF \
        "$@"
}

cmake_build /src/libjpeg-turbo jpeg -DENABLE_SHARED=OFF -DWITH_TURBOJPEG=OFF -DWITH_TOOLS=OFF -DWITH_TESTS=OFF
cmake --build jpeg -j"$(nproc)"
cmake --install jpeg

cmake_build /src/libde265 de265 -DENABLE_SDL=OFF -DENABLE_DECODER=OFF -DENABLE_ENCODER=OFF
cmake --build de265 -j"$(nproc)"
cmake --install de265

cmake_build /src/libheif heif \
    -DWITH_LIBDE265=ON -DWITH_LIBDE265_PLUGIN=OFF \
    -DWITH_X265=OFF -DWITH_KVAZAAR=OFF -DWITH_UVG266=OFF -DWITH_VVDEC=OFF -DWITH_VVENC=OFF \
    -DWITH_X264=OFF -DWITH_OpenH264_DECODER=OFF \
    -DWITH_DAV1D=OFF -DWITH_AOM_DECODER=OFF -DWITH_AOM_ENCODER=OFF -DWITH_SvtEnc=OFF -DWITH_RAV1E=OFF \
    -DWITH_JPEG_DECODER=OFF -DWITH_JPEG_ENCODER=OFF \
    -DWITH_OpenJPEG_DECODER=OFF -DWITH_OpenJPEG_ENCODER=OFF -DWITH_OPENJPH_ENCODER=OFF \
    -DWITH_FFMPEG_DECODER=OFF -DWITH_WEBCODECS=OFF -DWITH_UNCOMPRESSED_CODEC=OFF \
    -DWITH_LIBSHARPYUV=OFF -DWITH_HEADER_COMPRESSION=OFF \
    -DWITH_GDK_PIXBUF=OFF -DWITH_EXAMPLES=ON -DWITH_EXAMPLE_HEIF_THUMB=OFF -DWITH_EXAMPLE_HEIF_VIEW=OFF \
    -DENABLE_PLUGIN_LOADING=OFF -DBUILD_TESTING=OFF -DBUILD_DOCUMENTATION=OFF \
    -DCMAKE_DISABLE_FIND_PACKAGE_TIFF=ON -DCMAKE_DISABLE_FIND_PACKAGE_PNG=ON -DCMAKE_DISABLE_FIND_PACKAGE_WEBP=ON \
    -DCMAKE_EXE_LINKER_FLAGS="-static -s"
cmake --build heif -j"$(nproc)" --target heif-dec

bin=heif/examples/heif-dec
"${cross}readelf" -d "$bin" 2>/dev/null | grep -q NEEDED && { echo "heif-dec is not static" >&2; exit 1; }
gzip -9n <"$bin" >"/out/linux_$arch.gz"
ls -l "$bin" "/out/linux_$arch.gz"
