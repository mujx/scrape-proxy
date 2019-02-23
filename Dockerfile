FROM golang:1.11-alpine as builder

RUN mkdir -p /app

WORKDIR /go/src/github.com/mujx/scrape-proxy

COPY . /go/src/github.com/mujx/scrape-proxy

COPY docker/run.sh /app

RUN apk --no-cache add make dep

RUN \
    make all && \
    cp ./client/client /app/client && \
    cp ./server/server /app/server && \
    chmod +x /app/client && \
    chmod +x /app/server && \
    chmod +x /app/run.sh


FROM alpine:latest

ENV RUN_MODE          server
ENV CLIENT_PROXY_ENDPOINT    "tcp://localhost:5050"
ENV CLIENT_REMOTE_FQDN       "localhost"

ENV SERVER_CLIENT_LISTEN_ADDR "tcp://*:5050"
ENV SERVER_WEB_LISTEN_ADDR    ":8080"
ENV SERVER_SURVEY_TIMEOUT     10

COPY --from=builder /app /app

CMD ["/app/run.sh"]
