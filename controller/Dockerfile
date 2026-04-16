FROM golang:1.22-alpine AS builder

WORKDIR /app

# 安装依赖
RUN apk add --no-cache git

# go mod - 使用国内代理
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download && go mod tidy

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o controller ./cmd/server

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/controller .
COPY --from=builder /app/migrations ./migrations

EXPOSE 8443

ENV GIN_MODE=release
ENV DATABASE_URL=postgres://shanlian:shanlian@localhost:5432/shanlian?sslmode=disable
ENV REDIS_URL=redis://localhost:6379/0
ENV JWT_SECRET=shanlian-jwt-secret-change-in-production

CMD ["./controller"]
