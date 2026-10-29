#!/usr/bin/env bash
set -euo pipefail
version="${1:?version required}"
arch="${2:?Go architecture required}"
case "$arch" in
    386) debarch=i386; tuple=i386-linux-gnu; compiler=i686-linux-gnu;;
    mipsle) debarch=mipsel; tuple=mipsel-linux-gnu; compiler="$tuple";;
    mips64le) debarch=mips64el; tuple=mips64el-linux-gnuabi64; compiler="$tuple";;
    loong64) debarch=loong64; tuple=loongarch64-linux-gnu; compiler="$tuple";;
    *) echo "Unsupported cross-build target: $arch" >&2; exit 1;;
esac
dpkg --add-architecture "$debarch"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    "gcc-$compiler" "g++-$compiler" pkg-config file ca-certificates \
    "libc6-dev:$debarch" "libgtk-3-dev:$debarch" "libwebkit2gtk-4.1-dev:$debarch" "libx11-dev:$debarch"
export CC="$compiler-gcc" CXX="$compiler-g++"
export PKG_CONFIG_LIBDIR="/usr/lib/$tuple/pkgconfig:/usr/share/pkgconfig"
bash scripts/build.sh "$version" "$arch"
