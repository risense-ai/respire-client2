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
    "gcc-$compiler" "g++-$compiler" pkg-config file ca-certificates python3
# Target introspection tools cannot be installed alongside the host's Python/tools.
# Resolve an empty target root, then unpack packages without running their scripts.
sysroot="$PWD/.tools/sysroot-$arch"
archives="$PWD/.tools/debs-$arch"
mkdir -p "$sysroot" "$archives/partial"
apt-get -o Dir::State::status=/dev/null -o APT::Architecture="$debarch" \
    -o APT::Architectures="$debarch" -o Dir::Cache::archives="$archives" \
    --download-only --yes --no-install-recommends install libc6-dev libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev
for package in "$archives"/*.deb; do dpkg-deb -x "$package" "$sysroot"; done
# Absolute loader symlinks must resolve inside the extracted root, not the host.
while IFS= read -r -d '' link; do
    target="$(readlink "$link")"
    relative="$(realpath -m -s --relative-to="$(dirname "$link")" "$sysroot$target")"
    ln -sfn "$relative" "$link"
done < <(find "$sysroot" -type l -lname '/*' -print0)
export CC="$compiler-gcc --sysroot=$sysroot" CXX="$compiler-g++ --sysroot=$sysroot"
export PKG_CONFIG_SYSROOT_DIR="$sysroot"
export PKG_CONFIG_LIBDIR="$sysroot/usr/lib/$tuple/pkgconfig:$sysroot/usr/share/pkgconfig"
bash scripts/build.sh "$version" "$arch"
