# ── 构建阶段 ──
FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o queenbee .

# ── 运行阶段 ──
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /build/queenbee /usr/local/bin/queenbee

ENV QUEENBEE_HOME=/data
VOLUME /data

EXPOSE 3777

ENTRYPOINT ["queenbee"]
CMD ["start"]
