# scrape-proxy

[![CircleCI](https://circleci.com/gh/mujx/scrape-proxy/tree/master.svg?style=svg)](https://circleci.com/gh/mujx/scrape-proxy/tree/master)

scrape-proxy enables scraping of Prometheus metrics from hosts that are not
directly accessible from Prometheus (e.g behind NAT).

## Usage

#### Server

It will start listening on `localhost:5050` for clients and on `localhost:8080`
will be the HTTP endpoint for Prometheus.

```bash
docker run --rm -e RUN_MODE=server \
    -e SERVER_CLIENT_LISTEN_ADDR="tcp://localhost:5050" \
    -e SERVER_WEB_LISTEN_ADDR=":8080" \
    -p 5050:5050 -p 8080:8080 \
    mujx/scrape-proxy
```

#### Client

It will connect on the server at `proxy_host:5050` and it will forward any
incoming scrape requests to `example.org`.

```bash
docker run --rm \
    -e RUN_MODE=client \
    -e CLIENT_PROXY_ENDPOINT="tcp://proxy_host:5050" \
    -e CLIENT_REMOTE_FQDN=example.org \
    mujx/scrape-proxy
```

## Deployment

A setup with docker-compose has been created to test the utility locally. It
consists of a Prometheus instance, a scrape-proxy server, and two scrape-proxy
clients that forward requests to an example application.

```bash
docker-compose up --build proxy client_1 client_2 sample_app

# Get the list of registered clients on the proxy.
curl -s http://localhost:8080/clients | jq

# Once the list is populated, save the results, so Prometheus can read the client list.
curl -s http://localhost:8080/clients > docker/clients.json

# Finally start the Prometheus instance and navigate to http://localhost:9090/targets.
docker-compose up prometheus
```
