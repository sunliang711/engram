FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/memory-server ./cmd/memory-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates git tzdata
COPY --from=builder /out/memory-server /usr/local/bin/memory-server

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/memory-server"]
