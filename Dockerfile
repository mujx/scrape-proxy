FROM golang:1.11-alpine3.9 as builder

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

ENV CLIENT_PROXY_ENDPOINT  "tcp://localhost:5050"
ENV CLIENT_REMOTE_FQDN     "localhost"

ENV SERVER_CLIENT_LISTEN_ADDR "tcp://*:5050"
ENV SERVER_WEB_LISTEN_ADDR    ":8080"
ENV SERVER_SURVEY_TIMEOUT     10

COPY --from=builder /app /app

CMD ["/app/run.sh"]
