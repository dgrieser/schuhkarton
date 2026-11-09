# A static heif-dec (libheif with only libde265 and libjpeg-turbo) for the
# Linux release builds, which embed it (internal/gallery/heifdec). It reads
# the HEICs of newer iPhones that the libheif of older systems refuses, at
# native speed.
#
#   docker buildx build --output type=local,dest=internal/gallery/heifdec deploy/heif-dec
#
# writes linux_amd64.gz, linux_arm64.gz and linux_arm.gz (armv7, hard float).
FROM debian:trixie AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl cmake make pkgconf nasm \
        gcc g++ \
        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu \
        gcc-arm-linux-gnueabihf g++-arm-linux-gnueabihf \
    && rm -rf /var/lib/apt/lists/*

ARG LIBHEIF=1.23.7
ARG LIBHEIF_SHA256=f7483e818503ea6224c8863e4a56ac02c7089021a98472c811c12aa19faf2475
ARG LIBDE265=1.1.3
ARG LIBDE265_SHA256=554228bd17788c99a7e63b37ab5634722190e6e2bf60c1dcb01cef328e133905
ARG LIBJPEG_TURBO=3.2.1
ARG LIBJPEG_TURBO_SHA256=c27ad72a68da151177929aa969ae773a2da5ac2af9ce946079314fdac8cece7f

WORKDIR /src
RUN set -eu; \
    get() { \
        curl -fsSL -o "$1.tar.gz" "$2"; \
        echo "$3  $1.tar.gz" | sha256sum -c -; \
        mkdir "$1"; tar xzf "$1.tar.gz" -C "$1" --strip-components 1; \
    }; \
    get libheif "https://github.com/strukturag/libheif/releases/download/v${LIBHEIF}/libheif-${LIBHEIF}.tar.gz" "$LIBHEIF_SHA256"; \
    get libde265 "https://github.com/strukturag/libde265/releases/download/v${LIBDE265}/libde265-${LIBDE265}.tar.gz" "$LIBDE265_SHA256"; \
    get libjpeg-turbo "https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/${LIBJPEG_TURBO}/libjpeg-turbo-${LIBJPEG_TURBO}.tar.gz" "$LIBJPEG_TURBO_SHA256"

COPY build.sh /build.sh
RUN sh /build.sh amd64 x86_64 "" \
    && sh /build.sh arm64 aarch64 aarch64-linux-gnu- \
    && sh /build.sh arm armv7l arm-linux-gnueabihf-

FROM scratch
COPY --from=build /out/ /
