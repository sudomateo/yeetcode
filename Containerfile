FROM golang:1.24.0 AS builder

ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -v -trimpath -ldflags='-extldflags=-static -w -s' .

FROM debian:bookworm-slim

RUN apt-get update -y && apt-get install -y ca-certificates

COPY --from=builder /app/yeetcode /usr/bin/yeetcode

CMD ["yeetcode"]
