FROM golang:1.12-alpine3.9 as builder

RUN mkdir -p /app

ADD . /go/src/github.com/mujx/scrape-proxy

WORKDIR /go/src/github.com/mujx/scrape-proxy

COPY docker/run.sh /app

RUN apk --no-cache add make dep git

RUN \
    dep ensure && \
    make static && \
    cp ./server/server /app && chmod +x /app/server && \
    cp ./client/client /app && chmod +x /app/client && \
    chmod +x /app/run.sh


FROM alpine:3.9

ENV RUN_MODE server

ENV CLIENT_PROXY_URL   "http://localhost:8080"
ENV CLIENT_REMOTE_FQND "localhost"
ENV CLIENT_HEARTBEAT   "10s"
ENV CLIENT_LOG_LEVEL   "info"

ENV SERVER_WEB_URL      ":8080"
ENV SERVER_TIMEOUT      "30s"
ENV SERVER_POLL_TIMEOUT "15s"
ENV SERVER_LOG_LEVEL    "info"

COPY --from=builder /app /app

CMD ["/app/run.sh"]
