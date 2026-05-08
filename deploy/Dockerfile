# Stage 1: build the server binary
FROM golang:1.22-alpine AS builder
WORKDIR /src

# Copy workspace and both modules
COPY go.work go.work.sum* ./
COPY server/ ./server/
COPY client/go.mod ./client/go.mod

WORKDIR /src/server
RUN go build -ldflags="-s -w" -o /remotemaster-server .

# Stage 2: minimal runtime image
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /remotemaster-server /usr/local/bin/remotemaster-server

EXPOSE 8080
ENV SERVER_ADDR=:8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/remotemaster-server"]
