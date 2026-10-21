#!/bin/sh
#
# Install the llm-proxy binary from GitHub Releases.
#
# Usage:
#   install.sh [-h | --help] [VERSION]
#
#   VERSION   Release tag to install, such as v1.1.0 or 1.1.0.
#             Default: the latest release. Prereleases are skipped.
#
# Examples:
#   # Install the latest release.
#   curl -fsSL https://raw.githubusercontent.com/TheLonelyGhost/llm-proxy/main/scripts/install.sh | sh
#
#   # Install a specific version. Put arguments after "sh -s --".
#   curl -fsSL https://raw.githubusercontent.com/TheLonelyGhost/llm-proxy/main/scripts/install.sh | sh -s -- v1.1.0
#
#   # Use wget when curl is not installed.
#   wget -qO- https://raw.githubusercontent.com/TheLonelyGhost/llm-proxy/main/scripts/install.sh | sh
#
# Behaviour:
#   - Installs to /usr/local/bin when run as root, otherwise to ~/.local/bin.
#   - Replaces an existing llm-proxy at that path, with a warning on stderr.
#     Also warns when another llm-proxy in PATH can take precedence.
#   - Downloads with curl, or with wget when curl is not installed.
#   - Verifies the binary against the release SHA256SUMS with sha256sum,
#     gsha256sum, shasum, or sha256. Stops on a mismatch, or when the release
#     has no SHA256SUMS. Warns, with an install hint, when no tool is found.
#   - Stops with a "go install" command when the release has no binary for
#     this OS and CPU. On Windows, download the .exe from the releases page.
#
# Written for POSIX sh. Functions with a subshell body, "f() ( ... )", keep
# their variables local without the non-POSIX "local" keyword.

set -eu

REPO='TheLonelyGhost/llm-proxy'
MODULE='github.com/thelonelyghost/llm-proxy'
BINARY='llm-proxy'

# Set by main; read by cleanup.
TMP_DIR=''
STAGED=''

log() { printf '%s\n' "$*" >&2; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$STAGED" ]; then rm -f "$STAGED"; fi
  if [ -n "$TMP_DIR" ]; then rm -rf "$TMP_DIR"; fi
}

usage() {
  cat >&2 <<EOF
Usage: install.sh [VERSION]

Install ${BINARY} from https://github.com/${REPO}/releases.

  VERSION   Release tag to install, such as v1.1.0 or 1.1.0.
            Default: the latest release (prereleases are skipped).

Install directory: /usr/local/bin for root, ~/.local/bin for other users.
EOF
}

# detect_platform prints a short platform family name. The name selects the
# package manager in the install hints.
detect_platform() (
  case "$(uname -s)" in
    Darwin) echo macos ;;
    FreeBSD) echo freebsd ;;
    OpenBSD) echo openbsd ;;
    Linux)
      if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091 # Read at run time on the target host.
        id_list="$(. /etc/os-release && echo "${ID:-} ${ID_LIKE:-}")"
      else
        id_list=''
      fi
      case " ${id_list} " in
        *' alpine '*) echo alpine ;;
        *' debian '* | *' ubuntu '*) echo debian ;;
        *' fedora '* | *' rhel '* | *' centos '*) echo fedora ;;
        *) echo linux ;;
      esac
      ;;
    *) echo unknown ;;
  esac
)

# sudo_prefix prints the command prefix that gives root access, or nothing
# when the script already runs as root.
sudo_prefix() {
  if [ "$(id -u)" -eq 0 ]; then
    return 0
  elif command -v sudo >/dev/null 2>&1; then
    printf 'sudo '
  elif command -v doas >/dev/null 2>&1; then
    printf 'doas '
  fi
}

# install_hint PLATFORM KIND prints the command that installs a missing tool.
# KIND is "http" (curl) or "checksum" (a SHA-256 tool).
install_hint() (
  platform="$1"
  kind="$2"
  root="$(sudo_prefix)"
  case "${platform}:${kind}" in
    macos:http) echo "curl is part of macOS. Install a separate copy with Homebrew: brew install curl" ;;
    macos:checksum) echo "shasum is part of macOS. Install GNU coreutils with Homebrew: brew install coreutils" ;;
    freebsd:http) echo "${root}pkg install -y curl" ;;
    freebsd:checksum) echo "sha256 is part of the FreeBSD base system. Install GNU coreutils: ${root}pkg install -y coreutils" ;;
    openbsd:http) echo "${root}pkg_add curl" ;;
    openbsd:checksum) echo "sha256 is part of the OpenBSD base system. Install GNU coreutils: ${root}pkg_add coreutils" ;;
    debian:http) echo "${root}apt-get update && ${root}apt-get install -y curl" ;;
    debian:checksum) echo "${root}apt-get update && ${root}apt-get install -y coreutils" ;;
    fedora:http) echo "${root}dnf install -y curl" ;;
    fedora:checksum) echo "${root}dnf install -y coreutils" ;;
    alpine:http) echo "${root}apk add curl" ;;
    alpine:checksum) echo "${root}apk add coreutils" ;;
    *:http) echo "Install curl (preferred) or wget with your system package manager." ;;
    *) echo "Install GNU coreutils (sha256sum) with your system package manager." ;;
  esac
)

# detect_http_tool prints "curl" or "wget", preferring curl.
detect_http_tool() {
  if command -v curl >/dev/null 2>&1; then
    echo curl
  elif command -v wget >/dev/null 2>&1; then
    echo wget
  fi
}

# fetch TOOL URL DEST downloads URL to DEST.
fetch() (
  tool="$1"
  url="$2"
  dest="$3"
  case "$tool" in
    curl) curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$dest" "$url" ;;
    wget) wget -q -O "$dest" "$url" ;;
  esac
)

# detect_checksum_tool prints the name of a SHA-256 tool, or nothing.
detect_checksum_tool() (
  for tool in sha256sum gsha256sum shasum sha256; do
    if command -v "$tool" >/dev/null 2>&1; then
      echo "$tool"
      exit 0
    fi
  done
)

# verify_sha256 TOOL PLATFORM DIR ASSET DIGEST checks DIR/ASSET against
# DIGEST with the check mode of TOOL. It returns non-zero on a mismatch.
verify_sha256() (
  tool="$1"
  platform="$2"
  dir="$3"
  asset="$4"
  digest="$5"
  list="${asset}.sha256"
  cd "$dir" || exit 1
  case "$tool" in
    sha256sum | gsha256sum)
      printf '%s  %s\n' "$digest" "$asset" >"$list"
      "$tool" -c "$list"
      ;;
    shasum)
      printf '%s  %s\n' "$digest" "$asset" >"$list"
      shasum -a 256 -c "$list"
      ;;
    sha256)
      # OpenBSD reads a checklist with -C. FreeBSD compares with -c.
      if [ "$platform" = openbsd ]; then
        printf 'SHA256 (%s) = %s\n' "$asset" "$digest" >"$list"
        sha256 -C "$list" "$asset"
      else
        sha256 -c "$digest" "$asset"
      fi
      ;;
  esac >/dev/null
)

# expected_sha256 SUMS_FILE ASSET prints the digest listed for ASSET.
expected_sha256() (
  sums="$1"
  asset="$2"
  while read -r sum name || [ -n "$sum" ]; do
    # sha256sum marks binary-mode entries with a leading '*'.
    if [ "$name" = "$asset" ] || [ "$name" = "*$asset" ]; then
      printf '%s\n' "$sum"
      exit 0
    fi
  done <"$sums"
  exit 1
)

# latest_version TOOL DIR prints the tag of the latest release. The GitHub
# "latest release" endpoint skips drafts and prereleases.
latest_version() (
  tool="$1"
  file="$2/latest.json"
  fetch "$tool" "https://api.github.com/repos/${REPO}/releases/latest" "$file" ||
    die "Cannot get the latest release from the GitHub API. Specify a version as an argument, such as: sh -s -- v1.1.0"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      *'"tag_name"'*)
        # Take the first quoted string after the key: "tag_name": "v1.1.0"
        tag=${line#*'"tag_name"'}
        tag=${tag#*'"'}
        tag=${tag%%'"'*}
        [ -n "$tag" ] || break
        printf '%s\n' "$tag"
        exit 0
        ;;
    esac
  done <"$file"
  die "Cannot find the latest release tag in the GitHub API response."
)

# normalize_version VERSION prints VERSION as a release tag.
normalize_version() (
  v="$1"
  case "$v" in
    '' | *[!A-Za-z0-9._-]*) die "Invalid version '${v}'. Use a release tag, such as v1.1.0." ;;
    [0-9]*) printf 'v%s\n' "$v" ;;
    *) printf '%s\n' "$v" ;;
  esac
)

# detect_target prints the release asset suffix "<os>-<arch>" with Go's GOOS
# and GOARCH names. A value with no mapping is kept as uname prints it; the
# release lookup then finds no binary and prints the build-from-source hint.
detect_target() (
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    FreeBSD) os=freebsd ;;
    OpenBSD) os=openbsd ;;
    NetBSD) os=netbsd ;;
    DragonFly) os=dragonfly ;;
    MINGW* | MSYS* | CYGWIN*)
      die "Windows is not supported by this script. Download ${BINARY}-windows-<arch>.exe from https://github.com/${REPO}/releases."
      ;;
  esac
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64) arch=arm64 ;;
    i386 | i486 | i586 | i686) arch=386 ;;
    armv6* | armv7*) arch=arm ;;
  esac
  printf '%s-%s\n' "$os" "$arch"
)

main() {
  version=''
  while [ $# -gt 0 ]; do
    case "$1" in
      -h | --help)
        usage
        exit 0
        ;;
      -*)
        usage
        die "Unknown option: $1"
        ;;
      *)
        [ -z "$version" ] || die "Too many arguments. Specify one version."
        version="$1"
        ;;
    esac
    shift
  done

  platform="$(detect_platform)"
  target="$(detect_target)"

  http_tool="$(detect_http_tool)"
  [ -n "$http_tool" ] ||
    die "This script needs curl or wget to download files. To install curl: $(install_hint "$platform" http)"

  TMP_DIR="$(mktemp -d)"
  trap cleanup EXIT
  # Turn signals into a normal exit so that the EXIT trap runs in all shells.
  trap 'exit 1' HUP INT TERM

  if [ -n "$version" ]; then
    version="$(normalize_version "$version")"
  else
    version="$(latest_version "$http_tool" "$TMP_DIR")"
  fi

  asset="${BINARY}-${target}"
  base_url="https://github.com/${REPO}/releases/download/${version}"
  build_hint="To build from source with Go, run: CGO_ENABLED=0 go install ${MODULE}/cmd/${BINARY}@${version}"

  checksum_tool="$(detect_checksum_tool)"
  if [ -z "$checksum_tool" ]; then
    warn "No SHA-256 tool found (sha256sum, gsha256sum, shasum or sha256). The download will not be verified."
    warn "To install one: $(install_hint "$platform" checksum)"
  fi

  # SHA256SUMS lists every binary in the release, so it also tells whether
  # the release has a binary for this platform.
  want=''
  if fetch "$http_tool" "${base_url}/SHA256SUMS" "${TMP_DIR}/SHA256SUMS"; then
    want="$(expected_sha256 "${TMP_DIR}/SHA256SUMS" "$asset")" ||
      die "Release ${version} has no prebuilt binary for ${target}. ${build_hint}"
  elif [ -n "$checksum_tool" ]; then
    die "Cannot download SHA256SUMS for release ${version}. Make sure that ${version} is a release of ${REPO} that includes SHA256SUMS."
  else
    warn "Cannot download SHA256SUMS for release ${version}. Trying to download the binary directly."
  fi

  log "Downloading ${asset} ${version}..."
  fetch "$http_tool" "${base_url}/${asset}" "${TMP_DIR}/${asset}" ||
    die "Cannot download ${base_url}/${asset}. Release ${version} possibly has no prebuilt binary for ${target}. ${build_hint}"

  if [ -n "$checksum_tool" ]; then
    verify_sha256 "$checksum_tool" "$platform" "$TMP_DIR" "$asset" "$want" ||
      die "Checksum mismatch for ${asset}: expected ${want}. Do not use this download."
    log "Verified SHA-256 checksum with ${checksum_tool}."
  fi

  if [ "$(id -u)" -eq 0 ]; then
    install_dir='/usr/local/bin'
  else
    [ -n "${HOME:-}" ] || die "HOME is not set. Cannot find the install directory ~/.local/bin."
    install_dir="${HOME}/.local/bin"
  fi
  dest="${install_dir}/${BINARY}"

  if [ -e "$dest" ]; then
    current="$("$dest" version </dev/null 2>/dev/null || true)"
    warn "${BINARY} is already installed at ${dest}${current:+ (${current})}. Replacing it with ${version}."
  fi
  existing="$(command -v "$BINARY" 2>/dev/null || true)"
  if [ -n "$existing" ] && [ "$existing" != "$dest" ]; then
    warn "Another ${BINARY} is at ${existing}. It can take precedence over ${dest} in your PATH."
  fi

  mkdir -p "$install_dir"
  # Stage the binary with its final mode in the install directory, then
  # rename it over the destination. A rename in one directory is atomic, so
  # the destination is never missing or partly written.
  STAGED="$(mktemp "${install_dir}/.${BINARY}.XXXXXX")"
  install -m 0755 "${TMP_DIR}/${asset}" "$STAGED"
  mv -f "$STAGED" "$dest"
  STAGED=''

  log "Installed ${BINARY} ${version} to ${dest}."

  case ":${PATH:-}:" in
    *":${install_dir}:"*) ;;
    *)
      warn "${install_dir} is not in your PATH. Add this line to your shell startup file (such as ~/.profile):"
      warn "  export PATH=\"${install_dir}:\$PATH\""
      ;;
  esac
}

# All work happens in main, so a partial download of this script through a
# pipe does not run.
main "$@"
