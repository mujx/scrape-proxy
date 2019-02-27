# scrape-proxy

[![CircleCI](https://circleci.com/gh/mujx/scrape-proxy/tree/master.svg?style=svg)](https://circleci.com/gh/mujx/scrape-proxy/tree/master)

scrape-proxy enables scraping of Prometheus metrics from hosts that are not
directly accessible from Prometheus (e.g behind NAT).

## Usage

An example Prometheus configuration in order to enable `scrape-proxy` can be
found in `docker/prometheus.yml`.

#### Server

```bash
docker run --rm -e RUN_MODE=server \
    -e SERVER_WEB_URL=":8080" \
    -p 8080:8080 \
    mujx/scrape-proxy
```

#### Client

```bash
docker run --rm \
    -e RUN_MODE=client \
    -e CLIENT_PROXY_URL="http://proxy_host:8080" \
    -e CLIENT_REMOTE_FQDN=example.org \ # Where to forward the proxy requests.
    mujx/scrape-proxy
```

## Local deployment

A setup with docker-compose has been created to test the utility locally. It
consists of a Prometheus instance, a scrape-proxy server, and two scrape-proxy
clients that forward requests to an example application.

```bash
docker-compose up --build --scale client=5 proxy client sample_app

# Once the list is populated, save the results, so Prometheus can read the client list.
curl -s http://localhost:8080/clients | jq | tee docker/clients.json

# Finally start the Prometheus instance and navigate to http://localhost:9090/targets.
docker-compose up prometheus
```
