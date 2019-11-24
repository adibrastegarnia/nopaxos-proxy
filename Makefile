export CGO_ENABLED=0
export GO111MODULE=on

.PHONY: build

ATOMIX_NOPAXOS_SEQUENCER_VERSION := latest

all: build

build: # @HELP build the source code
build:
	GOOS=linux GOARCH=amd64 go build -o build/_output/atomix-nopaxos-sequencer ./cmd/atomix-nopaxos-sequencer

test: # @HELP run the unit tests and source code validation
test: build license_check linters
	go test github.com/atomix/atomix-nopaxos-sequencer/...

coverage: # @HELP generate unit test coverage data
coverage: build linters license_check
	go test github.com/atomix/atomix-nopaxos-sequencer/pkg/... -coverprofile=coverage.out.tmp -covermode=count
	@cat coverage.out.tmp | grep -v ".pb.go" > coverage.out

linters: # @HELP examines Go source code and reports coding problems
	golangci-lint run

license_check: # @HELP examine and ensure license headers exist
	./build/licensing/boilerplate.py -v

proto: # @HELP build Protobuf/gRPC generated types
proto:
	docker run -it -v `pwd`:/go/src/github.com/atomix/atomix-nopaxos-sequencer \
		-w /go/src/github.com/atomix/atomix-nopaxos-sequencer \
		--entrypoint build/bin/compile_protos.sh \
		onosproject/protoc-go:stable

image: # @HELP build atomix-nopaxos-sequencer Docker image
image: build
	docker build . -f build/docker/Dockerfile -t atomix/atomix-nopaxos-sequencer:${ATOMIX_NOPAXOS_SEQUENCER_VERSION}

push: # @HELP push atomix-nopaxos-sequencer Docker image
	docker push atomix/atomix-nopaxos-sequencer:${ATOMIX_NOPAXOS_SEQUENCER_VERSION}
