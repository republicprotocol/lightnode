package lightnode

import (
	"fmt"
	"net/http"
	"time"

	"github.com/renproject/lightnode/p2p"
	"github.com/renproject/lightnode/resolver"
	"github.com/renproject/lightnode/rpc"
	"github.com/renproject/lightnode/store"
	"github.com/republicprotocol/darknode-go/server/jsonrpc"
	"github.com/republicprotocol/renp2p-go/core/peer"
	"github.com/republicprotocol/renp2p-go/foundation/addr"
	"github.com/republicprotocol/tau"
	"github.com/rs/cors"
	"github.com/sirupsen/logrus"
)

// Lightnode defines the fields required by the server.
type Lightnode struct {
	port     string
	logger   logrus.FieldLogger
	handler  http.Handler
	resolver tau.Task
}

// NewLightnode constructs a new Lightnode.
func NewLightnode(logger logrus.FieldLogger, cap, workers, timeout int, port string, addresses []addr.Addr) *Lightnode {
	lightnode := &Lightnode{
		port:   port,
		logger: logger,
	}

	// Construct client and server.
	addrStore := store.NewCache(0)
	client := rpc.NewClient(logger, cap, workers, time.Duration(timeout)*time.Second, addrStore)
	requests := make(chan jsonrpc.Request, cap)
	jsonrpcService := jsonrpc.New(logger, requests, time.Duration(timeout)*time.Second)
	server := rpc.NewServer(logger, cap, requests)
	bootstrapAddrs := make([]peer.MultiAddr, len(addresses))
	for i, addr := range addresses {
		multiAddr, err := peer.NewMultiAddr(addr.String(), 0, [65]byte{})
		if err != nil {
			logger.Fatalf("invalid bootstrap addresses: %v", err)
		}
		bootstrapAddrs[i] = multiAddr
	}
	p2pService := p2p.New(logger, cap, time.Duration(timeout)*time.Second, addrStore, bootstrapAddrs)
	lightnode.handler = cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
		AllowedMethods:   []string{"GET", "POST", "OPTIONS", "DELETE"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		Debug:            true,
	}).Handler(jsonrpcService)

	// Construct resolver.
	lightnode.resolver = resolver.New(cap, logger, client, server, p2pService, addresses)

	return lightnode
}

// Run starts listening for requests using a HTTP server.
func (node *Lightnode) Run(done <-chan struct{}) {
	node.logger.Infof("JSON-RPC server listening on 0.0.0.0:%v...", node.port)
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%v", node.port), node.handler); err != nil {
			node.logger.Errorf("failed to serve: %v", err)
		}
	}()

	go node.resolver.Run(done)
	node.resolver.IO().InputWriter() <- p2p.Tick{}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			node.logger.Debug("updating darknode multi addresses")
			node.resolver.IO().InputWriter() <- p2p.Tick{}
		}
	}
}
