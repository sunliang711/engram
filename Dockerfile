FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

ARG VERSION=
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X engram/internal/version.Version=${VERSION}" -o /out/engram-server ./cmd/engram-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates git tzdata
COPY --from=builder /out/engram-server /usr/local/bin/engram-server

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/engram-server"]
