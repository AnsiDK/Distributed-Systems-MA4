# Ricart–Agrawala Mutual Exclusion in Go (gRPC)

This repository implements distributed mutual exclusion using the Ricart–Agrawala algorithm. Nodes communicate via gRPC to coordinate entry into a emulated critical section.

Key properties:
- R1 (Spec): Any node can request access to the critical section at any time (via manual input or automatic intervals).
- R2 (Safety): At most one node enters the critical section at a time.
- R3 (Liveness): Every requesting node eventually gains access (assuming peers are reachable and responsive).

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
```
go run ./cmd/node --id n1 --addr :50051 --peers peers.json --auto --interval 8s
```

Terminal 2:
```
go run ./cmd/node --id n2 --addr :50052 --peers peers.json
```

Terminal 3:
```
go run ./cmd/node --id n3 --addr :50053 --peers peers.json
```

- When not in `--auto` mode, press Enter in a node’s terminal to request the critical section.
- Logs show requests, replies, deferrals, entries, and exits.

## How it Works

- Each node is both a gRPC server and a client to other nodes.
- Nodes maintain:
  - Lamport clock
  - State: RELEASED, REQUESTING, HELD
  - Waiting set (pending replies)
  - Deferred set (peers to reply to after leaving the CS)

Ricart–Agrawala logic:
- To request CS access:
  1. Increment Lamport clock, record the request timestamp.
  2. Send `Request(from_id, timestamp)` to all peers.
  3. Wait for `Reply` from all peers, then enter CS.

- On `Request` from peer:
  - Reply immediately if:
    - Not requesting, OR
    - Your request’s timestamp is greater than the sender’s (or equal but your ID is greater).
  - Otherwise, defer reply (send it after leaving CS).

- On `Reply`:
  - Remove sender from the waiting set. When empty, enter CS.

- Exiting CS:
  - Reply to all deferred peers and clear the deferred set.

## Demonstrating Requirements

- R1: Any node can request access. Use auto mode or press Enter in manual mode.
- R2: Safety. Entering the CS requires all other peers’ replies; ties are resolved by (Lamport timestamp, node ID).
- R3: Liveness. Given peers eventually respond, each requester will collect all replies and enter.

Example log excerpt:
```
[Node n1] ... REQUEST CS ts=17 peers=[n2,n3]
[Node n2] ... RECV Request from=n1 ts=17 state=RELEASED ... ALLOW (reply now) to=n1
[Node n3] ... RECV Request from=n1 ts=17 state=REQUESTING ... DEFER reply to=n1
[Node n1] ... RECV Reply from=n2 ts=18 (remaining=1)
[Node n3] ... EXIT CS; sending deferred=[n1]
[Node n1] ... RECV Reply from=n3 ts=26 (remaining=0)
[Node n1] ... ENTER CS state=HELD clock=27 ...
[Node n1] ... CRITICAL SECTION: write: node=n1 at time=...
[Node n1] ... EXIT CS; sending deferred=[...]
```

## Service Discovery

This example uses a simple JSON file (`peers.json`) with the list of nodes:
```
[
  { "id": "n1", "addr": "127.0.0.1:50051" },
  { "id": "n2", "addr": "127.0.0.1:50052" },
  { "id": "n3", "addr": "127.0.0.1:50053" }
]
```

Alternative discovery options:
- Hardcode peers in code
- Pass peers via command-line flags
- Use a service registry (e.g., Consul, etcd) — not required for this assignment

## Notes

- For simplicity, the node dials peers per RPC with a timeout. In production, you may pool persistent connections.
- Liveness (R3) assumes connectivity and no permanent failures. You can add retries/backoff for robustness.
- The critical section is emulated by logging and sleeping.

## Report Guidance (Hand-in)

Include in your PDF (max 2 pages):
- How R1, R2, R3 are achieved (refer to the algorithm and your logs).
- Discuss tie-breaking and Lamport clocks.
- Append logs from a run showing a node requesting, receiving replies, entering, and exiting the CS.
- Include a link to your Git repository.
