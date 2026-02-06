# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.24.0
ARG APP_VERSION=dev
ARG APP_COMMIT=dev
ARG APP_COMMIT_SHORT=dev
ARG BUILD_DATE=unknown
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

ARG APP_VERSION
ARG APP_COMMIT
ARG APP_COMMIT_SHORT
ARG BUILD_DATE

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache ca-certificates build-base

# Cache module downloads (before declaring TARGETARCH to avoid re-downloading for each platform)
COPY go.mod go.sum ./
RUN go mod download

# Declare target platform arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH

# Copy source and build binary using Go cross-compilation
COPY . ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X github.com/ldbl/sre/backend/pkg/version.Version=${APP_VERSION} -X github.com/ldbl/sre/backend/pkg/version.Commit=${APP_COMMIT} -X github.com/ldbl/sre/backend/pkg/version.ShortCommit=${APP_COMMIT_SHORT} -X github.com/ldbl/sre/backend/pkg/version.BuildDate=${BUILD_DATE}" \
    -o /out/backend ./cmd/api

FROM alpine:3.20

ARG APP_VERSION
ARG APP_COMMIT
ARG APP_COMMIT_SHORT
ARG BUILD_DATE

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

COPY --from=build /out/backend /usr/local/bin/backend
USER app
EXPOSE 8080
ENV PORT=8080 \
    APP_VERSION=${APP_VERSION} \
    APP_COMMIT=${APP_COMMIT} \
    APP_COMMIT_SHORT=${APP_COMMIT_SHORT} \
    APP_BUILD_DATE=${BUILD_DATE}

ENTRYPOINT ["/usr/local/bin/backend"]
