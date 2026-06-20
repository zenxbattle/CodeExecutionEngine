FROM golang:1.24.1-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git
RUN --mount=type=secret,id=GIT_AUTH_TOKEN \
    git config --global url."https://oauth2:$(cat /run/secrets/GIT_AUTH_TOKEN)@github.com/".insteadOf "https://github.com/"

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /app/engine ./cmd

FROM alpine:latest

ENV DOCKER_API_VERSION=1.44

RUN apk add --no-cache ca-certificates && \
    wget -qO /tmp/docker.tgz https://download.docker.com/linux/static/stable/aarch64/docker-28.0.4.tgz && \
    tar -xzf /tmp/docker.tgz -C /usr/local/bin/ --strip-components=1 docker/docker && \
    rm /tmp/docker.tgz

WORKDIR /app
COPY --from=builder /app/engine .
EXPOSE 50053

CMD ["./engine"]
