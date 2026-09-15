// Chat: the neutral message store (universal-social-prd §4d). One Postgres
// table of {from, to, kind, body, sent_at}; the core's friendship answer at
// the door; every send announced on the core's universal event stream so each
// family's adapter delivers it in its own transport. Ports: 20130 HTTP,
// 20131 gRPC, 20132 metrics (ports.md, block 20130).
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"

	"google.golang.org/grpc"

	api "openpak/chat/internal/api"
	"openpak/chat/internal/core"
	"openpak/chat/internal/grpcapi"
	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
	chatv1 "openpak/chat/proto/openpak/chat/v1"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()

	pool, err := store.DB(ctx, env("CHAT_DATABASE_URL", "postgres://postgres:test@127.0.0.1:54329/chat?sslmode=disable"))
	if err != nil {
		log.Fatalf("chat: database: %v", err)
	}
	defer pool.Close()

	coreClient, err := core.Dial(env("CHAT_CORE_ADDRESS", "127.0.0.1:20001"), os.Getenv("CHAT_CORE_INTERNAL_KEY"))
	if err != nil {
		log.Fatalf("chat: %v", err)
	}

	svc := &service.Service{Store: pool, Core: coreClient}
	internalKey := os.Getenv("CHAT_INTERNAL_KEY")

	// HTTP: the website's surface.
	mux := http.NewServeMux()
	(&api.Server{Svc: svc, Key: internalKey}).Register(mux)
	httpAddr := env("CHAT_HTTP_ADDR", "127.0.0.1:20130")
	httpSrv := &http.Server{Addr: httpAddr, Handler: mux}
	go func() {
		log.Printf("chat: http on %s", httpAddr)
		log.Fatal(httpSrv.ListenAndServe())
	}()

	// gRPC: the console-side adapters' surface.
	grpcAddr := env("CHAT_GRPC_ADDR", "127.0.0.1:20131")
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("chat: grpc listen: %v", err)
	}
	gs := grpc.NewServer()
	chatv1.RegisterChatServer(gs, grpcapi.New(svc, internalKey))
	go func() {
		log.Printf("chat: grpc on %s", grpcAddr)
		log.Fatal(gs.Serve(lis))
	}()

	// Metrics: this deployment has none beyond the process itself yet; keep
	// the port claimed so the block stays 20130..20139.
	metricsAddr := env("CHAT_METRICS_ADDR", "127.0.0.1:20132")
	go func() {
		m := http.NewServeMux()
		m.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		log.Fatal(http.ListenAndServe(metricsAddr, m))
	}()

	select {} // serve until killed
}
