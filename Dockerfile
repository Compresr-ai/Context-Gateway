FROM golang:1.23-alpine AS builder
RUN apk add --no-cache make
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN make embed-prep && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway ./cmd

FROM alpine:3.21
RUN addgroup -g 1000 gateway && adduser -u 1000 -G gateway -D gateway
WORKDIR /app
COPY --from=builder /build/gateway .
RUN mkdir -p /app/logs && chown -R gateway:gateway /app
USER gateway
EXPOSE 18080
ENTRYPOINT ["./gateway"]
CMD ["serve", "--no-banner"]
