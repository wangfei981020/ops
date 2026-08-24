#!/usr/bin/env bash
#
# 镜像构建的唯一入口。
#
# 存在的理由只有一个：**把版本号注入收口到一处**。
# 上一代四个前端都是各建各的 docker build，结果全都漏传过 --build-arg VERSION，
# 线上跑着的前端在「关于」页显示 dev，排障时看不出是哪一版。
# 版本号不该靠人记得传。
#
# 用法：
#   tooling/scripts/build-image.sh 某个同类产品 v12                    # 前端（默认）
#   tooling/scripts/build-image.sh 另一个产品  v12 backend            # 后端
#   tooling/scripts/build-image.sh 某个同类产品 v12                    # 前端（默认）
#   tooling/scripts/build-image.sh 另一个产品  v12 backend            # 后端 --push        # 推 Harbor（单架构）
#   tooling/scripts/build-image.sh 某个同类产品 v12                    # 前端（默认）
#   tooling/scripts/build-image.sh 另一个产品  v12 backend            # 后端 --push-remote # 推 yourorg（默认只 amd64）
#   ... --push-remote --multiarch                                   # 需要 arm64 时才加
#   ... --release                                                   # 发布：构建 amd64 → 扫这一份 → 通过才推 yourorg

set -euo pipefail

PRODUCT="${1:-}"
VERSION="${2:-}"

# ⚠️ MODE 必须扫描全部参数，不能写 ${3:-}。
# 加了组件参数之后，`build-image.sh 某个同类产品 v1 frontend --push` 里
# 第 3 个参数是 "frontend"，--push 排在第 4 位 ——
# 取 $3 的话 MODE 恒为组件名，永远匹配不上 --push，
# 于是脚本**只本地构建、不推送，却照样打印「✓ 完成」**。
# 表现是 helm 部署 ImagePullBackOff，而你以为镜像早就推上去了。
MODE=""
MULTIARCH=0
for a in "$@"; do
  case "$a" in
    --push|--push-remote|--release) MODE="$a" ;;
    # 远端默认只推 amd64（生产就是 amd64）。要 arm64 时显式加这个
    --multiarch) MULTIARCH=1 ;;
  esac
done

if [[ -z "$PRODUCT" || -z "$VERSION" ]]; then
  echo "用法: $0 <产品> <版本> [frontend|backend] [--push|--push-remote|--release] [--multiarch]" >&2
  exit 1
fi

# 语义化版本，防止把分支名之类的东西当版本传进来。
# 规范见 ops/VERSIONING.md。
#   v0.1.0          开发期（v0.x 不承诺兼容性）
#   v1.0.0          正式版
#   v1.2.0-rc.1     发布候选；alpha / beta / rc 三档
SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.[1-9][0-9]*)?$'
if [[ ! "$VERSION" =~ $SEMVER ]]; then
  echo "✗ 版本号不合规：$VERSION" >&2
  echo "  正确形如：v0.1.0 / v1.0.0 / v1.2.0-rc.1（预发布只允许 alpha|beta|rc）" >&2
  echo "  规范见 ops/VERSIONING.md" >&2
  exit 1
fi

# 预发布版本禁止推生产仓库：alpha/beta/rc 是给内部和试用客户的，
# 一旦进了对外仓库，就没法阻止别人拿它当正式版部署。
if [[ ( "$MODE" == "--push-remote" || "$MODE" == "--release" ) && "$VERSION" == *-* ]]; then
  echo "✗ 预发布版本 $VERSION 不能推到对外仓库" >&2
  echo "  本地验证用不带 --push-remote 的形式，或先出正式版本号" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# 组件：frontend（默认）| backend。
#
# 加这一层是因为脚本原来只管前端 —— 而版本号注入这道防线当初就是为了防
# 「落成 dev」，后端不走脚本的话，这道防线对后端等于没建。
# SSO 的网关与后端**共用同一个镜像**（只换 command），所以不是三个组件而是两个。
COMPONENT="frontend"
COMPONENT_GIVEN=0
for a in "$@"; do
  case "$a" in
    frontend|backend) COMPONENT="$a"; COMPONENT_GIVEN=1 ;;
  esac
done

# ⚠️ 不给组件参数时默认只构建 frontend —— 而 ops/ 的约定是
# 「前后端共用同一个 tag，只改一侧也要两边一起发」（feedback: ops 共享版本 tag）。
# 默认值与约定相反，且原来不吭声：以为发了一整套，实际只发了前端一半，
# 部署时后端仍是旧镜像，或者 helm 指到一个根本不存在的 tag 直接 ImagePullBackOff。
# 不改默认行为（其他产品可能依赖它），但必须说出来。
if [[ "$COMPONENT_GIVEN" == "0" ]]; then
  echo "⚠️ 未指定组件，本次只构建 frontend。"
  echo "   约定要求前后端同 tag 一起发，别忘了另一半："
  echo "   $0 $PRODUCT $VERSION backend ${MODE}"
  echo
fi

case "$COMPONENT" in
  frontend)
    # 前端要在仓库根构建：Dockerfile 里要 COPY 各个共享包的 package.json
    CONTEXT="$ROOT"
    DOCKERFILE="$ROOT/$PRODUCT/frontend/Dockerfile"
    ;;
  backend)
    DOCKERFILE="$ROOT/$PRODUCT/backend/Dockerfile"
    # 上下文按**这个后端有没有 replace 到仓内共享包**来定，不能一刀切：
    #
    #   有 replace（如某产品 → 内部依赖包）
    #     上下文必须是 ops/，否则那个目录不在里面，报
    #     「failed to compute cache key: "/某个同类产品/backend": not found」
    #
    #   无 replace（如本产品，无内部依赖包）
    #     Dockerfile 里是 `COPY go.mod go.sum ./`，上下文给 ops/ 的话
    #     根本没有 /go.sum，报「"/go.sum": not found」
    #
    # 两种报错都指向文件不存在，谁也看不出真因是上下文给宽了还是给窄了。
    # 所以让脚本自己判断，而不是让人记住哪个产品要哪种。
    if grep -qE '^\s*replace .*=> \.\./\.\.' "$ROOT/$PRODUCT/backend/go.mod" 2>/dev/null; then
      CONTEXT="$ROOT"
    else
      CONTEXT="$ROOT/$PRODUCT/backend"
    fi
    ;;
esac

if [[ ! -f "$DOCKERFILE" ]]; then
  echo "✗ 找不到 $DOCKERFILE" >&2
  exit 1
fi

GIT_COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [[ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ]]; then
  # 带脏工作区构建出来的镜像无法从 commit 回溯到确切代码，标记出来
  GIT_COMMIT="${GIT_COMMIT}-dirty"
fi

LOCAL_IMAGE="registry.example.com/ops/${PRODUCT}-${COMPONENT}:${VERSION}"
REMOTE_IMAGE="yourorg/${PRODUCT}-${COMPONENT}:${VERSION}"

echo "产品      $PRODUCT"
echo "组件      $COMPONENT"
echo "版本      $VERSION"
echo "commit    $GIT_COMMIT"
echo

case "$MODE" in
  --push-remote)
    # 远端（yourorg）是**送生产的通道**，生产 GKE 全是 linux/amd64，
    # 所以默认只推 amd64。
    #
    # 🔴 这里原来固定推 amd64+arm64，理由写的是"本地是 arm64"——但那条理由不成立：
    # 本地部署拉的是 registry.example.com 的 Harbor（见下面的 --push 分支，原生 arm64 构建），
    # **从来不从 yourorg 拉**。于是那份 arm64 上传上去没有任何消费者。
    #
    # 代价是实打实的：后端的 Go 二进制约 60MB 且每次改代码必变，
    # 双架构等于每次多传一份 60MB。实测一次后端推送要 10 分钟，
    # 其中构建只占约 14 秒（其余全是缓存命中），剩下全在 pushing layers。
    #
    # ⚠️ 真需要 arm64 时（比如要在 Mac 上直接 docker pull 这个镜像跑），
    # 加 --multiarch。默认不带，是因为那属于例外而不是常态。
    PLATFORMS="linux/amd64"
    if [[ "$MULTIARCH" == "1" ]]; then
      PLATFORMS="linux/amd64,linux/arm64"
      echo "→ 多架构构建并推送 ${REMOTE_IMAGE}（${PLATFORMS}）"
    else
      echo "→ 构建并推送 ${REMOTE_IMAGE}（${PLATFORMS}，生产只用 amd64；要 arm64 加 --multiarch）"
    fi
    docker buildx build \
      --platform "$PLATFORMS" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "GIT_COMMIT=$GIT_COMMIT" \
      -f "$DOCKERFILE" -t "$REMOTE_IMAGE" \
      --push "$CONTEXT"
    ;;
  --release)
    # 发布通道：**构建一次 amd64 → 扫这一份 → 通过才推**。
    #
    # # 它修的是两个真问题
    #
    # ① 🔴 **扫的和推的不是同一个镜像**（这条比"慢"严重得多）
    #
    #	原来的做法是：`--push` 构建**原生 arm64** 推本地 Harbor → 拿那份扫 Trivy →
    #	`--push-remote` 再构建一次 **amd64** 推 yourorg。
    #	实测确认过：
    #	    本地(被扫的那份)   linux/arm64
    #	    yourorg(上生产的)  linux/amd64
    #	arm64 干净**不代表** amd64 干净 —— 基础镜像里两个架构的包版本可以不同。
    #	等于扫描这道关卡放行的是另一个产物。
    #
    # ② 重复构建
    #
    #	两条路的构建缓存**完全不共享**（架构不同），所以每次发布都要跨架构全量重建一次。
    #
    # # 为什么用 --load
    #
    # buildx 直接 --push 的话，镜像不落本地，Trivy 就只能扫远端 —— 那就成了"先推后扫"，
    # 有问题时东西已经在对外仓库里了。--load 把 amd64 那份装进本地 docker，
    # 扫完再 docker push **同一个** tag，全程只构建一次。
    #
    # ⚠️ --load 只能装单架构，所以本模式不支持 --multiarch（真要多架构，
    #	 说明那是例外场景，走 --push-remote --multiarch，并自行确认扫描口径）。
    if [[ "$MULTIARCH" == "1" ]]; then
      echo "✗ --release 不支持 --multiarch：--load 只能装单架构镜像" >&2
      echo "  发布通道固定 linux/amd64（生产 GKE 就是 amd64）。" >&2
      echo "  确需多架构请用 --push-remote --multiarch，并自行确认扫描的是哪一份" >&2
      exit 1
    fi

    LOG_DIR="${TMPDIR:-/tmp}/ops-build-logs"
    mkdir -p "$LOG_DIR"
    LOG="$LOG_DIR/${PRODUCT}-${COMPONENT}-${VERSION}.log"

    echo "→ 发布构建 ${REMOTE_IMAGE}（linux/amd64，构建一次、扫这一份、通过才推）"
    echo "  完整日志: $LOG"
    echo

    # ⚠️ 完整日志必须留下。此前排查"这次推送为什么花了 20 分钟"时，
    #	因为把输出 tail 掉了，只能靠猜 —— 猜出来的原因还是错的。
    t0=$(date +%s)
    docker buildx build \
      --platform linux/amd64 \
      --build-arg "VERSION=$VERSION" \
      --build-arg "GIT_COMMIT=$GIT_COMMIT" \
      -f "$DOCKERFILE" -t "$REMOTE_IMAGE" \
      --load "$CONTEXT" 2>&1 | tee "$LOG"
    t1=$(date +%s)

    # 前端额外要 npm/pnpm 依赖漏洞清零（约定：前端 audit + 镜像 Trivy 双清）
    if [[ "$COMPONENT" == "frontend" ]]; then
      echo
      echo "→ pnpm audit（前端依赖）"
      ( cd "$ROOT" && pnpm audit --audit-level low ) || {
        echo "✗ pnpm audit 有未处置的漏洞，已中止 —— **镜像没有推**" >&2
        exit 1
      }
    fi

    echo
    echo "→ Trivy 扫描 ${REMOTE_IMAGE}（全严重度；扫的就是即将推出去的这一份）"
    # ⚠️ 两组参数**属于不同的程序**，必须分开：
    #	MOUNT_ARGS 给 docker run（-v 挂载）
    #	TRIVY_ARGS 给 trivy（--ignorefile）
    # 混在一个数组里，--ignorefile 会跑到 docker 的参数位上，
    # 报 "unknown flag: --ignorefile" —— 实测第一次跑就是这么挂的。
    # ⚠️ 展开必须写成 ${ARR[@]+"${ARR[@]}"}。
    #	macOS 自带 bash 3.2，`set -u` 下直接展开**空数组**会报 "unbound variable"。
    #	前端没有 .trivyignore → 数组为空 → 当场挂掉，而后端有清单所以一路绿灯。
    #	🔴 教训：分支有两条（有清单/没清单），我只测了有清单那条就以为好了。
    MOUNT_ARGS=()
    TRIVY_ARGS=()
    IGNORE_FILE="$ROOT/$PRODUCT/$COMPONENT/.trivyignore"
    if [[ -f "$IGNORE_FILE" ]]; then
      # ⚠️ 变量名后面紧跟全角字符时**必须**用 ${}：bash 会把 UTF-8 那几个字节
      #	当成变量名的一部分，set -u 下直接报 "unbound variable"。
      #	实测第一次跑这条就炸在这里 —— 而且炸在扫描环节，前面的构建全白做。
      echo "  豁免清单: ${IGNORE_FILE}（每条都必须写明理由，见 ops/SECURITY-WAIVERS.md）"
      MOUNT_ARGS=(-v "$IGNORE_FILE:/tmp/.trivyignore")
      TRIVY_ARGS=(--ignorefile /tmp/.trivyignore)
    fi
    t2=$(date +%s)
    # --exit-code 1：扫出东西就让脚本失败。
    # 🔴 扫描必须能**拦住**发布，否则它只是一份没人看的报告。
    set +e
    docker run --rm \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v "$HOME/.cache/trivy:/root/.cache" \
      ${MOUNT_ARGS[@]+"${MOUNT_ARGS[@]}"} \
      aquasec/trivy:latest image \
      --scanners vuln \
      --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL \
      ${TRIVY_ARGS[@]+"${TRIVY_ARGS[@]}"} \
      --exit-code 1 --quiet "$REMOTE_IMAGE"
    SCAN_RC=$?
    set -e
    # 🔴 「扫出漏洞」和「扫描没跑起来」是两回事，绝不能混成一句话。
    #	Trivy 约定：exit 1 = 确实扫到了未豁免的漏洞；其它非 0 = 工具/参数/网络出错。
    #	第一版把两者都报成"发现未处置漏洞"，而实际是我把 --ignorefile 传给了 docker，
    #	扫描根本没执行 —— 照那句提示去查依赖，只会白查一整轮。
    if [[ $SCAN_RC -eq 1 ]]; then
      echo >&2
      echo "✗ Trivy 扫出未处置漏洞，已中止 —— **镜像没有推到 yourorg**" >&2
      echo "  修依赖，或在 $IGNORE_FILE 里登记豁免并写明理由（ops/SECURITY-WAIVERS.md）" >&2
      exit 1
    elif [[ $SCAN_RC -ne 0 ]]; then
      echo >&2
      echo "✗ Trivy **没能完成扫描**（退出码 $SCAN_RC），已中止 —— **镜像没有推到 yourorg**" >&2
      echo "  这不是漏洞问题，是扫描本身失败：检查参数、镜像是否已 load、网络能否拉漏洞库" >&2
      echo "  ⚠️ 别把它当成「没有漏洞」放行 —— 没扫成和扫干净是两回事" >&2
      exit 1
    fi
    t3=$(date +%s)

    echo
    echo "→ 推送 ${REMOTE_IMAGE}"
    docker push "$REMOTE_IMAGE" 2>&1 | tee -a "$LOG"
    t4=$(date +%s)

    echo
    echo "耗时  构建 $((t1-t0))s | 扫描 $((t3-t2))s | 推送 $((t4-t3))s | 合计 $((t4-t0))s"
    ;;

  --push)
    echo "→ 构建并推送 $LOCAL_IMAGE"
    docker build \
      --build-arg "VERSION=$VERSION" \
      --build-arg "GIT_COMMIT=$GIT_COMMIT" \
      -f "$DOCKERFILE" -t "$LOCAL_IMAGE" "$CONTEXT"
    docker push "$LOCAL_IMAGE"
    ;;
  *)
    echo "→ 仅本地构建 $LOCAL_IMAGE"
    docker build \
      --build-arg "VERSION=$VERSION" \
      --build-arg "GIT_COMMIT=$GIT_COMMIT" \
      -f "$DOCKERFILE" -t "$LOCAL_IMAGE" "$CONTEXT"
    ;;
esac

echo
echo "✓ 完成。确认镜像里的版本号："
# 两个组件的版本号落点不同：前端是构建产物里的 version.json，
# 后端是编译进二进制的 -X main.version。给错命令的话，
# 人会以为版本没注入进去，再去改一遍本来就没坏的东西。
if [[ "$COMPONENT" == "frontend" ]]; then
  echo "  docker run --rm ${LOCAL_IMAGE} cat /usr/share/nginx/html/version.json"
else
  echo "  docker run --rm ${LOCAL_IMAGE} -version"
fi
