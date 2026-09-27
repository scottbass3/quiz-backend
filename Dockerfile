# Production image: a static binary on distroless, running as non-root.
# Migrations are embedded in the binary and applied at startup.
# (Dockerfile.dev is the hot-reload development image used by docker-compose.yml.)

FROM golang:1.23-alpine AS build

WORKDIR /src

# Dependencies first, so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

USER nonroot:nonroot
ENV HTTP_ADDR=:8080
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 CMD ["/api", "healthcheck"]

ENTRYPOINT ["/api"]
