## Requirements

- Go 1.22+
- Protocol Buffers compiler `protoc`
- Protobuf plugins for Go:
  - `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
  - `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`

Ensure `$GOPATH/bin` (or your Go bin directory) is in your `PATH`.

# Run program

You can run nodes manually in separate terminals:

Terminal 1:

go run ./cmd/node --id n1 --addr :50051 --peers peers.json --auto --interval 8s


Terminal 2:

go run ./cmd/node --id n2 --addr :50052 --peers peers.json


Terminal 3:

go run ./cmd/node --id n3 --addr :50053 --peers peers.json
