FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /relay cmd/relay/main.go

FROM alpine:3.22
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /relay /usr/local/bin/relay

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/relay"]
