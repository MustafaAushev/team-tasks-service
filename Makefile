# Локальные команды проверки. Подробности — в README.md.
#
#   make test-deps-up     поднять тестовые MySQL и Redis (docker-compose.test.yml)
#   make test-unit        unit-тесты, зависимости не нужны
#   make test-integration MySQL/Redis integration (нужны test-deps)
#   make test-api         HTTP/API с проверкой по OpenAPI (нужны test-deps)
#   make check            vet + unit + race по всем наборам

# Значения совпадают с docker-compose.test.yml; переопределяются окружением.
export TEST_MYSQL_ADMIN_DSN ?= root:testroot@tcp(127.0.0.1:13306)/
export TEST_MYSQL_HOST      ?= 127.0.0.1
export TEST_MYSQL_PORT      ?= 13306
export TEST_MYSQL_USER      ?= tasks
export TEST_MYSQL_PASSWORD  ?= taskspw
export TEST_REDIS_ADDR      ?= 127.0.0.1:16379
export TEST_REDIS_EVICT_ADDR ?= 127.0.0.1:16380

# Дополнительные флаги go test, например: make test-api TESTFLAGS='-run TestLogin -v'
TESTFLAGS ?=

.PHONY: test-deps-up test-deps-down vet test-unit test-integration test-api test-race check e2e

test-deps-up:
	docker compose -f docker-compose.test.yml up -d --wait

test-deps-down:
	docker compose -f docker-compose.test.yml down -v

vet:
	go vet ./...
	go vet -tags "integration api e2e" ./...

# Без тегов: интеграционные и API-тесты не компилируются, внешние зависимости не нужны.
test-unit:
	go test -count=1 $(TESTFLAGS) ./...

# При недоступных MySQL/Redis набор завершается ошибкой, а не пропускается.
test-integration:
	go test -tags integration -count=1 $(TESTFLAGS) ./tests/integration/...

test-api:
	go test -tags api -count=1 $(TESTFLAGS) ./tests/api/...

test-race:
	go test -race -tags "integration api" -count=1 $(TESTFLAGS) ./...

check: vet test-unit test-race

# Сквозной сценарий через Docker Compose на отдельном стеке; тестовые зависимости не нужны.
e2e:
	./scripts/e2e.sh
