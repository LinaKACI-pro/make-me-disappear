.PHONY: all build vet lint staticcheck test check clean install-hooks

BIN_BOT    = bot
BIN_SCRAPE = scrape

all: check build

build:
	go build -o $(BIN_BOT) ./cmd/bot
	go build -o $(BIN_SCRAPE) ./cmd/scrape

vet:
	go vet ./...

lint:
	golangci-lint run ./...

staticcheck:
	staticcheck ./...

test:
	go test ./...

check: vet lint staticcheck test

clean:
	rm -f $(BIN_BOT) $(BIN_SCRAPE)

install-hooks:
	cp hooks/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "pre-commit hook installed"
