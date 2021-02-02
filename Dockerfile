FROM golang:latest AS builder
RUN go get github.com/hkwi/jqhttp/...

FROM alpine:latest
COPY --from=builder go/bin/jqhttp /usr/local/bin/jqhttp
CMD ["/usr/local/bin/jqhttp"]
