.PHONY: up down logs test test-race lint fmt benchmark

up:
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f api worker-1 worker-2

test:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine go test ./...

test-race:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine sh -c 'apk add --no-cache gcc musl-dev && go test -race ./...'

fmt:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine gofmt -w .

lint:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine sh -c 'go vet ./... && go test ./...'

benchmark:
	DOCWEAVE_ALLOW_PRIVATE_NETWORKS=true docker compose --profile benchmark up --build -d fixture postgres api worker-1 worker-2 prometheus
	@echo "Create a crawl for http://fixture:8081/page/0 with allowed host fixture, depth 3, and 1000 pages."
