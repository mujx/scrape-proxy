all: client server

client:
	cd client && go build

server:
	cd server && go build

image:
	docker build -t scrape-proxy .

ensure:
	dep ensure -update

.PHONY: client server
