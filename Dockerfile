ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -v -o /run-app .

FROM debian:bookworm
RUN apt-get update && \
  apt-get install -y ca-certificates ffmpeg && \
  rm -rf /var/lib/apt/lists/*
COPY --from=builder /run-app /usr/local/bin/

# Create volume directory for Railway volumes support
RUN mkdir -p /data/sounds

CMD ["run-app"]
