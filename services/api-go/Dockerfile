# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.25.7-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY services/api-go/go.mod services/api-go/go.sum ./services/api-go/
RUN cd services/api-go && go mod download
COPY services/api-go ./services/api-go
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN cd services/api-go && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/api /app/api
EXPOSE 8090
ENTRYPOINT ["/app/api"]
