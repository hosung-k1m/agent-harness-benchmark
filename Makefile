.PHONY: build build-cli build-agent build-verifier test fmt clean

build: build-cli build-agent build-verifier

build-cli:
	go build -o bench ./cmd/bench

build-agent:
	docker build -f docker/agent.Dockerfile -t agent-harness-benchmark:latest .

build-verifier:
	docker build -f docker/verifier.Dockerfile -t agent-harness-verifier:latest .

test:
	go test ./...
	go vet ./...
	python3 -m unittest discover -s docker/adapters -p 'test_*.py'
	python3 -m py_compile docker/adapters/normalize.py docker/verifier/verify.py
	sh -n docker/adapters/codex-adapter.sh docker/adapters/dsh-adapter.sh docker/adapters/archive-workspace.sh

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

clean:
	go clean
