FROM golang:1.24.1-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git
RUN --mount=type=secret,id=GIT_AUTH_TOKEN \
    git config --global url."https://oauth2:$(cat /run/secrets/GIT_AUTH_TOKEN)@github.com/".insteadOf "https://github.com/"

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /app/engine ./cmd

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/engine .
EXPOSE 50053

CMD ["./engine"]
