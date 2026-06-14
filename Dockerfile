# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jqhttp .

FROM alpine:3.24

RUN apk add --no-cache ca-certificates

COPY --from=builder /out/jqhttp /usr/local/bin/jqhttp

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/jqhttp"]
