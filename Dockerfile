# syntax=docker/dockerfile:1.7
FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/docweave ./cmd/docweave && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fixture ./cmd/fixture

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/docweave /docweave
COPY --from=build /out/fixture /fixture
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/docweave"]
