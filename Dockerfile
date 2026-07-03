# Build a static v2bx binary, then ship it on a minimal Alpine base with the
# CA certificates the panel client needs for HTTPS.
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/v2bx ./cmd/v2bx

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/v2bx /usr/local/bin/v2bx

# Config lives here; mount it as a volume.
VOLUME /etc/v2bx

ENTRYPOINT ["/usr/local/bin/v2bx"]
CMD ["server", "-c", "/etc/v2bx/config.json"]
