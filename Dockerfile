# syntax=docker/dockerfile:1

FROM node:22.22.0-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package.json ./
# The frontend currently has no lockfile. Do not generate one in the checkout.
RUN npm install --no-package-lock --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o /out/automain ./cmd/automain

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 automain \
    && useradd --uid 10001 --gid automain --no-create-home --home-dir /data --shell /usr/sbin/nologin automain \
    && install -d -o automain -g automain -m 0700 /data
COPY --from=builder /out/automain /usr/local/bin/automain
USER 10001:10001
WORKDIR /data
ENV AUTOMAIN_DB_PATH=/data/automain.db \
    AUTOMAIN_INDEX_DB_PATH=/data/index.db \
    AUTOMAIN_LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=6 \
    CMD curl --fail --silent --show-error --max-time 4 --output /dev/null http://127.0.0.1:8080/readyz
ENTRYPOINT ["/usr/local/bin/automain"]
CMD ["serve"]
