.PHONY: tidy test tag

## tidy: run go mod tidy
tidy:
	go mod tidy

## test: run all tests
test:
	go test ./... -v -race

## tag: create and push a semver tag  →  make tag VERSION=0.1.0
tag:
	@test -n "$(VERSION)" || (echo "Usage: make tag VERSION=x.y.z" && exit 1)
	git tag v$(VERSION)
	git push origin v$(VERSION)
	@echo "✅ Tagged and pushed v$(VERSION)"
