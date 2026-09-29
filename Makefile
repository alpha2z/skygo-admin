.PHONY: test build release scan web-test
export GOWORK=off
test:
	go test ./...
	go test -race ./internal/...
	$(MAKE) web-test
web-test:
	node --test admin-web/*.test.cjs
build:
	go build ./cmd/... ./examples/...
release:
	python3 scripts/release.py
scan:
	python3 scripts/scan.py
