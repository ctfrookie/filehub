# 构建阶段
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o filehub .

# 运行阶段
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && mkdir -p /app/data
WORKDIR /app
COPY --from=builder /app/filehub .
VOLUME ["/app/data"]
EXPOSE 8080
CMD ["./filehub"]