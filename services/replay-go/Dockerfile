# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY services/replay-go/go.mod services/replay-go/go.sum ./services/replay-go/
RUN cd services/replay-go && go mod download
COPY services/replay-go ./services/replay-go
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN cd services/replay-go && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/replay ./cmd/replay

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/replay /app/replay
ENTRYPOINT ["/app/replay"]
