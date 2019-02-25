all: client server

client:
	cd client && go build

server:
	cd server && go build

static:
	cd client && CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"'
	cd server && CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"'

image:
	docker build -t scrape-proxy .

ensure:
	dep ensure -update

.PHONY: client server
