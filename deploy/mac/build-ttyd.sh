#!/usr/bin/env bash
# Build the Mac upstream with WebSocket compression, independent of Homebrew lws.
set -euo pipefail

[[ "$(uname -s)" == Darwin ]] || { echo 'This build is for macOS.' >&2; exit 1; }
MAC_SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
BUILD_ROOT="${SHELL_GATE_BUILD_DIR:-${XDG_CONFIG_HOME:-${HOME}/.config}/shell-gate/cache/ttyd-build}"
RUNTIME="${HOME}/.local/share/shell-gate/ttyd-1.7.7-lws-4.3.10-deflate"
BREW_PREFIX="$(brew --prefix)"
for dependency in cmake libuv json-c openssl@3; do
  brew list --versions "$dependency" >/dev/null || brew install "$dependency"
done
mkdir -p "$BUILD_ROOT" "$RUNTIME/bin"

fetch_source() {
  local name="$1" repo="$2" tag="$3" expected="$4"
  if [[ ! -d "$BUILD_ROOT/$name/.git" ]]; then
    git -c advice.detachedHead=false clone --depth 1 --branch "$tag" "$repo" "$BUILD_ROOT/$name"
  fi
  [[ "$(git -C "$BUILD_ROOT/$name" rev-parse HEAD)" == "$expected" ]] || {
    echo "Unexpected source revision: $name" >&2; exit 1;
  }
  [[ -z "$(git -C "$BUILD_ROOT/$name" status --porcelain)" ]] || {
    echo "Source tree has local changes: $name" >&2; exit 1;
  }
}
fetch_source lws https://github.com/warmcat/libwebsockets.git v4.3.10 2288cf200bc1c28680765bd4f07e437356106c2d
fetch_source ttyd https://github.com/tsl0922/ttyd.git 1.7.7 40e79c706be14029b391f369bee6613c31667abb

# 5.0.0 with extensions reproduces ttyd #1532 (large compressed paste disconnects).
# Keep this compatibility pair until both directions pass check-ttyd-compression.py.
cmake -S "$BUILD_ROOT/lws" -B "$BUILD_ROOT/lws-build" \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$BUILD_ROOT/lws-install" -DCMAKE_PREFIX_PATH="$BREW_PREFIX" \
  -DLWS_WITHOUT_EXTENSIONS=OFF -DLWS_WITH_ZLIB=ON -DLWS_WITH_LIBUV=ON \
  -DLWS_WITH_EVLIB_PLUGINS=OFF -DLWS_WITH_SHARED=OFF -DLWS_WITH_STATIC=ON \
  -DLWS_WITHOUT_TESTAPPS=ON
cmake --build "$BUILD_ROOT/lws-build" -j 4
cmake --install "$BUILD_ROOT/lws-build"
# Upstream 4.3.10 exports a nonexistent shared target even in static-only builds.
python3 - "$BUILD_ROOT/lws-install/lib/cmake/libwebsockets/libwebsockets-config.cmake" <<'PYCONFIG'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
old = 'set(LIBWEBSOCKETS_LIBRARIES websockets websockets_shared)'
assert old in text, 'Unexpected libwebsockets export configuration'
path.write_text(text.replace(old, 'set(LIBWEBSOCKETS_LIBRARIES websockets)'))
PYCONFIG
cmake -S "$BUILD_ROOT/ttyd" -B "$BUILD_ROOT/ttyd-build" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_NO_SYSTEM_FROM_IMPORTED=ON \
  "-DCMAKE_PREFIX_PATH=$BUILD_ROOT/lws-install;$BREW_PREFIX" \
  -DLibwebsockets_DIR="$BUILD_ROOT/lws-install/lib/cmake/libwebsockets"
cmake --build "$BUILD_ROOT/ttyd-build" -j 4
python3 "$MAC_SCRIPTS/check-ttyd-compression.py" "$BUILD_ROOT/ttyd-build/ttyd"
# Static lws prevents a Homebrew update from silently disabling compression again.
if otool -L "$BUILD_ROOT/ttyd-build/ttyd" | grep -q 'libwebsockets.*dylib'; then
  echo 'Refusing a runtime linked to shared libwebsockets.' >&2; exit 1
fi
install -m 755 "$BUILD_ROOT/ttyd-build/ttyd" "$RUNTIME/bin/ttyd.new"
mv -f "$RUNTIME/bin/ttyd.new" "$RUNTIME/bin/ttyd"
mkdir -p "${HOME}/.local/bin"
ln -sfn "$RUNTIME/bin/ttyd" "${HOME}/.local/bin/shell-gate-ttyd"
echo "Installed $RUNTIME/bin/ttyd; restart the upstream only after verification."
