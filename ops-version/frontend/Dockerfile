# ⚠️ 构建上下文是 **ops/**（存放各产品的那一层），不是 frontend/。
#    前端要 pnpm workspace 才能解析 @ops/* 共享包，上下文给窄了会报
#    「Failed to resolve entry for package @ops/xxx」。
#    构建：tooling/scripts/build-image.sh ops-version <ver> frontend --push
#
# ---------- 构建 ----------
FROM --platform=$BUILDPLATFORM node:26-alpine AS builder

# 🔴 VERSION 必须传进来并进环境变量：vite.config 的 define 读的就是它。
#    漏传会让产物标成 dev，线上排障看不出前端是哪一版 —— 上一代四个前端全踩过。
#    build-image.sh 已经把这件事收口，别裸 docker build。
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ENV VERSION=$VERSION GIT_COMMIT=$GIT_COMMIT

# 版本与根 package.json 的 packageManager 保持一致 —— 不一致时 pnpm 会用
# 不同的 lockfile 解析规则，镜像里装出来的依赖和本地不完全一样
RUN npm i -g pnpm@11.20.0
WORKDIR /repo

# 先只拷清单，让依赖层可缓存。逐个列出来是为了这个缓存 ——
# 加新共享包时这里要跟着加一行，漏了的表现是 pnpm install 装不全，
# 而错误信息会指向业务代码
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY packages/design/package.json packages/design/
COPY packages/i18n/package.json packages/i18n/
COPY packages/ui/package.json packages/ui/
COPY ops-version/frontend/package.json ops-version/frontend/
RUN pnpm install --frozen-lockfile

COPY . .
# 共享包要先产出 dist，应用才能解析到它们的类型与入口。
# 用通配而不是逐个点名：漏掉一个时 vite 只会说「Failed to resolve entry for
# package @ops/xxx」，完全看不出是"没构建"。
# ⚠️ 本产品的 build 脚本串了 12 个守卫，任何一个不过都会在这里失败 —— 这是刻意的
RUN pnpm -r --filter './packages/*' build \
 && pnpm --filter ops-version-frontend build

# ---------- 运行 ----------
# 用 unprivileged 版：官方 nginx 镜像以 root 启动，而 chart 里设了 runAsNonRoot，
# 两者对不上 Pod 会 CrashLoop。这个镜像默认监听 8080，非特权端口。
# nginx.conf 由 chart 的 ConfigMap 挂进来，镜像只负责提供静态产物。
FROM nginxinc/nginx-unprivileged:1.29-alpine

ARG VERSION=dev
ARG GIT_COMMIT=unknown
LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$GIT_COMMIT \
      org.opencontainers.image.title="ops-version-frontend"

USER root
RUN apk upgrade --no-cache
COPY --from=builder /repo/ops-version/frontend/dist /usr/share/nginx/html
# 版本号同时落进静态文件：运行中的容器可以直接 curl /version.json 问它是哪一版，
# 比从镜像 tag 推断可靠（滚动更新中途新旧并存）
RUN printf '{"version":"%s","commit":"%s"}\n' "$VERSION" "$GIT_COMMIT" \
      > /usr/share/nginx/html/version.json \
 && chown -R 101:101 /usr/share/nginx/html
# ⚠️ 必须是**数字**，不能写 `USER nginx`：runAsNonRoot 下 kubelet 查不到名字对应的 uid，
# 直接报 "image has non-numeric user"，Pod 起不来。101 是该镜像里 nginx 用户的 uid
USER 101

EXPOSE 8080
