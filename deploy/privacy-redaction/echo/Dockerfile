# 受控回显上游（验证专用）
#
# 与 crg/Dockerfile 使用同一锁定基础镜像，避免多引入一个信任面。
ARG BASE_IMAGE=node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293

FROM ${BASE_IMAGE}

WORKDIR /app

COPY --chown=root:root server.mjs ./

RUN chmod 0444 server.mjs

USER node

ENV HOST=0.0.0.0 \
    PORT=8080 \
    NODE_ENV=production

EXPOSE 8080

CMD ["node", "server.mjs"]
