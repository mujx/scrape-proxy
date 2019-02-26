# scrape-proxy

[![CircleCI](https://circleci.com/gh/mujx/scrape-proxy/tree/master.svg?style=svg)](https://circleci.com/gh/mujx/scrape-proxy/tree/master)

scrape-proxy enables scraping of Prometheus metrics from hosts that are not
directly accessible from Prometheus (e.g behind NAT).

## Usage

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
