install: fmt clean grpc
	go mod download

fmt:
	gofmt -l -s -w .

grpc:
	protoc ./proto/dropbox.proto \
		--go_opt=paths=source_relative --go-grpc_opt=paths=source_relative \
		--go_out=. --go-grpc_out=.

build:

clean:
	@rm -rf dist/

.PHONY:fmt install grpc
