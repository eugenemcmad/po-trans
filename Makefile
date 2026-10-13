T   = docker compose -p po-trans-test
E2E = docker compose -p po-trans-e2e -f docker-compose.yml -f docker-compose.e2e.yml

.PHONY: test test-ci e2e lint docker-up docker-down

# Unit tests on the host: no Docker, no -race (that needs gcc).
test:
	go test ./internal/...

# Unit + integration tests with -race, in containers, in a separate project.
test-ci:
	$(T) --profile test run --rm --build tests || ($(T) --profile test down && exit 1)
	$(T) --profile test down

# Black-box tests against a freshly built environment, in a separate project.
e2e:
	$(E2E) --profile test down
	$(E2E) up -d --build || ($(E2E) --profile test down && exit 1)
	$(E2E) --profile test run --rm --build --no-deps e2e || ($(E2E) --profile test down && exit 1)
	$(E2E) --profile test down

lint:
	docker compose --profile test run --rm --build --no-deps tests golangci-lint run

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down
