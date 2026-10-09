.PHONY: up down test integration smoke benchmark vet

up:
	docker compose up --build -d --wait
down:
	docker compose down
test:
	go test -race ./...
integration:
	sh scripts/test-integration.sh
smoke:
	sh scripts/smoke.sh
benchmark:
	sh scripts/benchmark.sh
vet:
	go vet ./...
