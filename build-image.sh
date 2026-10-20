#!/usr/bin/env bash
# 构建 OpenList 镜像并推送到阿里云 registry
#
# 与仓库自带的 build.sh + Dockerfile 的区别：Dockerfile 里跑的是 build.sh release docker，
# 会从 OpenListTeam/OpenList-Frontend 的 release 下载 dist，盖掉本地前端。这里改成在
# OpenList-Frontend 子模块里本地 pnpm build，保证镜像里包含本仓库自己的前端改动。
#
# 用法：
#   ./build-image.sh                # 构建 + push :latest
#   ./build-image.sh --no-push      # 只构建
#   ./build-image.sh -t v1.2.3      # 额外打一个 tag
#   SKIP_FRONTEND=1 ./build-image.sh  # 前端没改动时跳过前端构建
set -euo pipefail

cd "$(dirname "$0")"

REGISTRY="registry.cn-hangzhou.aliyuncs.com/clansty/openlist-clansty"
IMAGE_NAME="openlist"
PLATFORM="${PLATFORM:-linux/amd64}"

TAGS=("latest")
PUSH=1
while [ $# -gt 0 ]; do
  case "$1" in
    -t | --tag) TAGS+=("$2"); shift 2 ;;
    --no-push) PUSH=0; shift ;;
    *)
      echo "未知参数: $1" >&2
      exit 1
      ;;
  esac
done

GOARCH="${PLATFORM#*/}"
BUILD_DIR="build/image"

# ---- 前端 ----
if [ "${SKIP_FRONTEND:-0}" != "1" ]; then
  # 前端仓库里 src/lang 除 en 之外的语言全被 gitignore，只有 en 时构建出来的界面没中文。
  # 官方 release（edge tag）附带了 i18n.tar.gz，里面是各语言的 json，拿下来解压进 src/lang 即可。
  if [ ! -d OpenList-Frontend/src/lang/zh-CN ] || [ "${REFRESH_I18N:-0}" = "1" ]; then
    echo "==> 拉取多语言文件"
    authArgs=()
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      authArgs=(-H "Authorization: Bearer $GITHUB_TOKEN")
    fi
    i18n_url="$(curl -fsSL "${authArgs[@]}" \
      "https://api.github.com/repos/OpenListTeam/OpenList-Frontend/releases/tags/edge" |
      grep -o '"browser_download_url": *"[^"]*i18n\.tar\.gz"' | head -1 | sed 's/.*"\(http.*\)"/\1/')"
    if [ -z "$i18n_url" ]; then
      echo "拿不到 i18n.tar.gz 地址，检查 GITHUB_TOKEN 或网络" >&2
      exit 1
    fi
    curl -fsSL "$i18n_url" | tar -xzf - -C OpenList-Frontend/src/lang
  fi

  echo "==> 构建前端"
  if [ ! -d OpenList-Frontend/node_modules ]; then
    (cd OpenList-Frontend && pnpm install --frozen-lockfile)
  fi
  # 补 entry.ts 和缺失的翻译文件，vite 靠 import.meta.glob("~/lang/*/index.json") 发现语言
  (cd OpenList-Frontend && node ./scripts/i18n.mjs && pnpm build)
  rm -rf public/dist
  mkdir -p public/dist
  cp -a OpenList-Frontend/dist/. public/dist/
  # public/dist/README.md 是仓库里唯一被跟踪的文件，别把它弄成 deleted
  git checkout -- public/dist/README.md 2>/dev/null || true
fi

if [ ! -d public/dist/assets ]; then
  echo "public/dist 为空，先跑一次前端构建（不要用 SKIP_FRONTEND=1）" >&2
  exit 1
fi

# ---- 后端 ----
builtAt="$(date +'%F %T %z')"
gitCommit="$(git log --pretty=format:'%h' -1)"
version="$(git describe --abbrev=0 --tags 2>/dev/null || echo v0.0.0)"
# 工作区有改动（比如本地 commit 之外还有未提交的东西）时带上 commit，免得和正式版混淆
if [ -n "$(git status --porcelain)" ]; then
  version="$version-$gitCommit"
fi
webVersion="$(node -p "require('./OpenList-Frontend/package.json').version")"

ldflags="\
-w -s \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.BuiltAt=$builtAt' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitAuthor=The OpenList Projects Contributors <noreply@oplist.org>' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitCommit=$gitCommit' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.Version=$version' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.WebVersion=$webVersion' \
"

echo "==> 构建后端 $PLATFORM (version=$version, webVersion=$webVersion, commit=$gitCommit)"
mkdir -p "$BUILD_DIR"
# linux/amd64 用的是纯 Go 的 glebarez sqlite，CGO_ENABLED=0 就能出静态二进制，也方便跨架构编译
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -tags jsoniter -trimpath -ldflags "$ldflags" -o "$BUILD_DIR/openlist" .

# ---- 镜像 ----
# 只把二进制和 entrypoint 丢进构建上下文，避免把 node_modules / .git 传给 docker
ctx="$(mktemp -d)"
trap 'rm -rf "$ctx"' EXIT
cp "$BUILD_DIR/openlist" "$ctx/openlist"
cp entrypoint.sh "$ctx/entrypoint.sh"

cat >"$ctx/Dockerfile" <<'EOF'
FROM openlistteam/openlist-base-image:base
LABEL MAINTAINER="OpenList"
ENV UMASK=022
WORKDIR /opt/openlist/
RUN addgroup -g 1001 openlist && \
    adduser -D -u 1001 -G openlist openlist && \
    mkdir -p /opt/openlist/data
COPY --chmod=755 --chown=1001:1001 openlist ./
COPY --chmod=755 --chown=1001:1001 entrypoint.sh /entrypoint.sh
USER openlist
VOLUME /opt/openlist/data/
EXPOSE 5244 5245
CMD [ "/entrypoint.sh" ]
EOF

tagArgs=()
for tag in "${TAGS[@]}"; do
  tagArgs+=(-t "$REGISTRY:$tag")
done

echo "==> 构建镜像 ${tagArgs[*]}"
docker build --platform "$PLATFORM" -f "$ctx/Dockerfile" "${tagArgs[@]}" "$ctx"

if [ "$PUSH" = "1" ]; then
  for tag in "${TAGS[@]}"; do
    echo "==> push $REGISTRY:$tag"
    docker push "$REGISTRY:$tag"
  done
else
  echo "==> 跳过 push（--no-push），镜像已打好：${tagArgs[*]}"
fi
