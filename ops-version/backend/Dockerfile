# ⚠️ 构建上下文是 **backend/ 本身**，不是 ops/。
#
#	tooling/scripts/build-image.sh 会按 go.mod 里有没有 `replace ... => ../..`
#	自动决定上下文：本后端没有 replace，所以给的是 backend/。
#	将来接了共享包，要同时改 go.mod 的 replace 和这里的 COPY 路径 ——
#	只改一处的报错都是「文件不存在」，看不出真因是上下文给宽了还是给窄了。
#
#	构建：tooling/scripts/build-image.sh ops-version v0.1.0 backend
#
# 多架构：构建层固定跑本机架构，用 GOOS/GOARCH 交叉编译（免 QEMU）
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

# 先只拷清单，让依赖层可缓存 —— 改业务代码不必重下依赖
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH
# VERSION 打进二进制：排障时能直接看出跑的是哪一版，
# 靠镜像 tag 推断在滚动更新中途不可靠
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/ops-version-backend .

FROM alpine:3.21
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates tzdata && \
    cp /usr/share/zoneinfo/Asia/Manila /etc/localtime
# 非 root：容器里跑的是只读采集 + 存着 Kite/Rancher 凭据的服务，
# 没有任何需要 root 的操作，给了就是白送一个提权面。
#
# ⚠️ uid **只在这里定义一次**，chart 只写 runAsNonRoot、不写 runAsUser（CONVENTIONS §4.5）。
# ⚠️ 必须是数字：写 `USER app` 的话 kubelet 在 runAsNonRoot 下查不到 uid，
#    直接报 image has non-numeric user，Pod 起不来。
RUN adduser -D -u 10001 app
COPY --from=build --chown=10001:10001 /out/ops-version-backend /app/ops-version-backend
WORKDIR /app
USER 10001
# 8080 业务 / 8088 健康检查（分开是为了业务端口 hang 死时探活仍可用）
EXPOSE 8080 8088
ENTRYPOINT ["/app/ops-version-backend"]
