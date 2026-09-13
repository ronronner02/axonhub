# CosyRedactGateway sidecar
#
# 构建（在 deploy/privacy-redaction/ 下）：
#   docker build -t axonhub-redact:local crg
#
# vendor 的 worker.js 零外部依赖，node-server.mjs 只用 Node 内置模块，
# 因此没有 npm install 阶段，也不需要 package.json。
#
# BASE_IMAGE 默认按 digest 锁定，保证可复现。升级基础镜像时同步更新此处并重跑验收。
#
# digest 取自 Docker Hub registry（多架构 OCI image index），解析日期 2026-09-12：
#   curl -sSI -H "Authorization: Bearer <token>" \
#     -H "Accept: application/vnd.oci.image.index.v1+json" \
#     https://registry-1.docker.io/v2/library/node/manifests/20-alpine
ARG BASE_IMAGE=node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293

FROM ${BASE_IMAGE}

# 以镜像自带的非 root 用户 node(1000) 运行。
WORKDIR /app

COPY --chown=root:root worker.js node-server.mjs LICENSE ./

# 只读根文件系统由 compose 施加；这里确保应用文件对运行用户不可写。
RUN chmod 0444 worker.js node-server.mjs LICENSE

USER node

ENV HOST=0.0.0.0 \
    PORT=8787 \
    NODE_ENV=production

EXPOSE 8787

# 不使用 shell 形式，避免多一层进程妨碍信号传递。
CMD ["node", "node-server.mjs"]
