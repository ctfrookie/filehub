# 构建阶段
# 使用滚动的大版本标签，构建时自动获取 Go 最新安全补丁
FROM golang:1-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o filehub .

# 运行阶段
# 同样使用滚动标签，确保基础系统安全补丁及时更新
FROM alpine:3
RUN apk add --no-cache ca-certificates && mkdir -p /app/data
WORKDIR /app
COPY --from=builder /app/filehub .
VOLUME ["/app/data"]
EXPOSE 8080
CMD ["./filehub"]