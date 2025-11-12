package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/AnsiDK/Distributed-systems-MA4/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

// Ricart-Agrawala states
type state int

const (
	Released state = iota
	Requesting
	Held
)

func (s state) String() string {
	switch s {
	case Released:
		return "RELEASED"
	case Requesting:
		return "REQUESTING"
	case Held:
		return "HELD"
	default:
		return "UNKNOWN"
	}
}

type peerInfo struct {
	ID   string `json:"id"`
	Addr string `json:"addr"`
}

type Node struct {
	pb.UnimplementedRAServiceServer

	// identity
	id   string
	addr string

	// peer discovery
	peers    map[string]string // id -> addr
	peerList []string          // stable ordered list of peer IDs (for logging)

	// lamport clock and mutex for state
	mu          sync.Mutex
	clock       int64
	curState    state
	requestTS   int64               
	waiting     map[string]struct{} 
	deferred    map[string]struct{} 
	grantCh     chan struct{}       
	inCS        bool                
	shutdownCh  chan struct{}       
	logger      *log.Logger
	prettyPeers string
}

func NewNode(id, addr string, peers map[string]string) *Node {
	// Copy peers excluding self
	p := make(map[string]string)
	var ids []string
	for pid, paddr := range peers {
		if pid == id {
			continue
		}
		p[pid] = paddr
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	return &Node{
		id:          id,
		addr:        addr,
		peers:       p,
		peerList:    ids,
		curState:    Released,
		waiting:     make(map[string]struct{}),
		deferred:    make(map[string]struct{}),
		logger:      log.New(os.Stdout, fmt.Sprintf("[Node %s] ", id), log.LstdFlags|log.Lmicroseconds),
		shutdownCh:  make(chan struct{}),
		prettyPeers: strings.Join(ids, ","),
	}
}

// lamportTick increments the local Lamport clock for a local event or before sending
func (n *Node) lamportTickLocked() int64 {
	n.clock++
	return n.clock
}

// lamportRecv updates Lamport clock on receiving a message with timestamp ts
func (n *Node) lamportRecvLocked(ts int64) int64 {
	if ts > n.clock {
		n.clock = ts
	}
	n.clock++
	return n.clock
}

func (n *Node) logf(format string, args ...any) {
	n.logger.Printf(format, args...)
}

func (n *Node) logStateLocked(prefix string) {
	n.logf("%s state=%s clock=%d waiting=%d deferred=%d", prefix, n.curState, n.clock, len(n.waiting), len(n.deferred))
}

func (n *Node) comparePriorityLocked(aTS int64, aID string, bTS int64, bID string) int {
	if aTS < bTS {
		return -1
	}
	if aTS > bTS {
		return 1
	}

	// tie-breaker by process ID
	if aID < bID {
		return -1
	}
	if aID > bID {
		return 1
	}
	return 0
}

func (n *Node) Request(ctx context.Context, req *pb.RequestMsg) (*pb.Ack, error) {
	var (
		shouldReply bool
		replyToID   string
		replyTS     int64
	)
	n.mu.Lock()
	n.lamportRecvLocked(req.Timestamp)
	n.logStateLocked(fmt.Sprintf("RECV Request from=%s ts=%d", req.FromId, req.Timestamp))

	deferReply := false
	if n.curState == Held {
		deferReply = true
	} else if n.curState == Requesting {
		if n.comparePriorityLocked(n.requestTS, n.id, req.Timestamp, req.FromId) <= 0 {
			deferReply = true
		}
	}

	if deferReply {
		n.deferred[req.FromId] = struct{}{}
		n.logStateLocked(fmt.Sprintf("DEFER reply to=%s", req.FromId))
	} else {
		shouldReply = true
		replyToID = req.FromId
		replyTS = n.lamportTickLocked() // sending reply increments clock
		n.logStateLocked(fmt.Sprintf("ALLOW (reply now) to=%s", req.FromId))
	}
	n.mu.Unlock()

	if shouldReply {
		// Send Reply outside lock
		if err := n.sendReply(replyToID, replyTS); err != nil {
			n.logf("ERROR sending Reply to=%s: %v", replyToID, err)
		} else {
			n.logf("SENT Reply to=%s ts=%d", replyToID, replyTS)
		}
	}

	return &pb.Ack{}, nil
}

func (n *Node) Reply(ctx context.Context, rep *pb.ReplyMsg) (*pb.Ack, error) {
	n.mu.Lock()
	n.lamportRecvLocked(rep.Timestamp)
	if _, ok := n.waiting[rep.FromId]; ok {
		delete(n.waiting, rep.FromId)
		n.logStateLocked(fmt.Sprintf("RECV Reply from=%s ts=%d (remaining=%d)", rep.FromId, rep.Timestamp, len(n.waiting)))
		if len(n.waiting) == 0 && n.curState == Requesting && n.grantCh != nil {
			// Signal that we can enter CS
			close(n.grantCh)
			n.grantCh = nil
		}
	} else {
		n.logStateLocked(fmt.Sprintf("RECV unexpected Reply from=%s ts=%d", rep.FromId, rep.Timestamp))
	}
	n.mu.Unlock()
	return &pb.Ack{}, nil
}

func (n *Node) sendRequest(toID string, ts int64) error {
	addr, ok := n.peers[toID]
	if !ok {
		return fmt.Errorf("unknown peer id %s", toID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewRAServiceClient(conn)
	_, err = client.Request(ctx, &pb.RequestMsg{FromId: n.id, Timestamp: ts})
	return err
}

func (n *Node) sendReply(toID string, ts int64) error {
	addr, ok := n.peers[toID]
	if !ok {
		return fmt.Errorf("unknown peer id %s", toID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewRAServiceClient(conn)
	_, err = client.Reply(ctx, &pb.ReplyMsg{FromId: n.id, Timestamp: ts})
	return err
}

// RequestCS initiates a request to enter the critical section following Ricart-Agrawala
func (n *Node) RequestCS() {
	// Initialize request
	n.mu.Lock()
	n.curState = Requesting
	reqTS := n.lamportTickLocked() // timestamp for this request
	n.requestTS = reqTS
	n.waiting = make(map[string]struct{}, len(n.peers))
	for pid := range n.peers {
		n.waiting[pid] = struct{}{}
	}
	n.grantCh = make(chan struct{})
	n.logStateLocked(fmt.Sprintf("REQUEST CS ts=%d peers=[%s]", reqTS, n.prettyPeers))
	n.mu.Unlock()

	// Send request to all peers concurrently
	var wg sync.WaitGroup
	for pid := range n.peers {
		pid := pid
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendRequest(pid, reqTS); err != nil {
				n.logf("ERROR send Request to=%s: %v", pid, err)
			} else {
				n.logf("SENT Request to=%s ts=%d", pid, reqTS)
			}
		}()
	}
	wg.Wait()

	// Wait until all replies received
	select {
	case <-n.grantCh:
		// granted
	case <-time.After(60 * time.Second):
		n.logf("TIMEOUT waiting for replies; will continue to wait in background")
		<-n.grantCh
	}

	// Enter CS
	n.mu.Lock()
	n.curState = Held
	n.inCS = true
	n.logStateLocked("ENTER CS")
	n.mu.Unlock()

	// Emulate critical section: exclusive access (R1), print/log and sleep
	n.criticalSection()

	// Exit CS
	n.mu.Lock()
	n.inCS = false
	n.curState = Released
	// Send deferred replies
	var toRelease []string
	for pid := range n.deferred {
		toRelease = append(toRelease, pid)
	}
	n.deferred = make(map[string]struct{})
	n.logStateLocked(fmt.Sprintf("EXIT CS; sending deferred=%v", toRelease))
	// Increment clock before batch of replies; each sendReply will use the same ts or we can tick per send.
	n.mu.Unlock()

	for _, pid := range toRelease {
		n.mu.Lock()
		ts := n.lamportTickLocked()
		n.mu.Unlock()
		if err := n.sendReply(pid, ts); err != nil {
			n.logf("ERROR sending deferred Reply to=%s: %v", pid, err)
		} else {
			n.logf("SENT deferred Reply to=%s ts=%d", pid, ts)
		}
	}
}

func (n *Node) criticalSection() {
	n.logf("CRITICAL SECTION: start")
	time.Sleep(2 * time.Second)
	n.logf("CRITICAL SECTION: write: node=%s at time=%s", n.id, time.Now().Format(time.RFC3339Nano))
	time.Sleep(1 * time.Second)
	n.logf("CRITICAL SECTION: end")
}

func (n *Node) serve() error {
	lis, err := net.Listen("tcp", n.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", n.addr, err)
	}
	s := grpc.NewServer()
	pb.RegisterRAServiceServer(s, n)
	// Enable reflection for easier debugging
	reflection.Register(s)

	n.logf("Starting gRPC server on %s; peers=[%s]", n.addr, n.prettyPeers)
	return s.Serve(lis)
}

func loadPeers(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []peerInfo
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.ID] = e.Addr
	}
	return out, nil
}

func main() {
	var (
		id       string
		addr     string
		peers    string
		auto     bool
		interval time.Duration
	)
	flag.StringVar(&id, "id", "", "node ID (e.g., n1)")
	flag.StringVar(&addr, "addr", ":50051", "listen address (e.g., :50051)")
	flag.StringVar(&peers, "peers", "peers.json", "path to peers JSON file")
	flag.BoolVar(&auto, "auto", false, "automatically request CS periodically")
	flag.DurationVar(&interval, "interval", 10*time.Second, "interval between automatic CS requests")
	flag.Parse()

	if id == "" {
		log.Fatalf("missing --id")
	}
	allPeers, err := loadPeers(peers)
	if err != nil {
		log.Fatalf("failed to load peers: %v", err)
	}
	if _, ok := allPeers[id]; !ok {
		log.Fatalf("node id %q not found in peers file", id)
	}
	node := NewNode(id, addr, allPeers)

	// Start server in background
	go func() {
		if err := node.serve(); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	// If auto mode, request CS periodically
	if auto {
		node.logf("Auto mode enabled: requesting CS every %s", interval)
		t := time.NewTicker(interval)
		defer t.Stop()
		// small initial jitter to avoid immediate synchronized collisions
		time.Sleep(time.Duration(500+int64(len(id))*97) * time.Millisecond)
		for {
			<-t.C
			go node.RequestCS()
		}
	} else {
		// Manual mode: press Enter to request CS
		node.logf("Manual mode: press Enter to request the Critical Section")
		for {
			_, err := fmt.Fscanln(os.Stdin)
			if err != nil {
				// On EOF or error, just trigger
			}
			go node.RequestCS()
		}
	}
}
