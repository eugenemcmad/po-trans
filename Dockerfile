# syntax=docker/dockerfile:1
# All stages run on Alpine 3.24; the target platform is linux/amd64.

FROM golang:1.26.9-alpine3.24 AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM deps AS build
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Test image: gcc for -race, the linter, all sources including tests, docs and SQL.
FROM deps AS test
ARG GOLANGCI_LINT_VERSION=2.14.0
ARG GOLANGCI_LINT_SHA256=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab
RUN apk add --no-cache build-base \
    && wget -q -O /tmp/golangci-lint.tar.gz \
       "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz" \
    && echo "${GOLANGCI_LINT_SHA256}  /tmp/golangci-lint.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/golangci-lint.tar.gz -C /usr/local/bin --strip-components=1 \
       "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint" \
    && rm /tmp/golangci-lint.tar.gz

FROM alpine:3.24 AS runtime
RUN adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
USER app
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/api"]
