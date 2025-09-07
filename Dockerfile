FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o fs-entangle .

FROM scratch

COPY --from=builder /app/fs-entangle /fs-entangle
RUN mkdir /data

EXPOSE 8080
ENTRYPOINT ["/fs-entangle", "server", "-d", "/data"]
