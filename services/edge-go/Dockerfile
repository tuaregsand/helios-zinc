# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.25.7-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY services/edge-go/go.mod ./services/edge-go/
RUN cd services/edge-go && go mod download
COPY services/edge-go ./services/edge-go
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN cd services/edge-go && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/edge ./cmd/edge

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/edge /app/edge
EXPOSE 8080
ENTRYPOINT ["/app/edge"]
