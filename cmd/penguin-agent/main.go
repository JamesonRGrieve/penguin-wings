// SPDX-License-Identifier: AGPL-3.0-or-later

// Command penguin-agent runs inside a Penguin LXC container. It supervises the
// game process and exposes console, logs, exit state, and stats to Wings over an
// authenticated HTTP + websocket API. The bearer token is provided via the
// PENGUIN_AGENT_TOKEN environment variable, injected at container creation.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pelican/wings/agent"
)

func main() {
	addr := flag.String("addr", ":8443", "address the agent API listens on")
	logLines := flag.Int("log-lines", 5000, "number of console output lines retained")
	flag.Parse()

	token := os.Getenv("PENGUIN_AGENT_TOKEN")
	if token == "" {
		log.Fatal("penguin-agent: PENGUIN_AGENT_TOKEN is required")
	}

	buf := agent.NewLineBuffer(*logLines)
	sup := agent.NewSupervisor(buf)
	srv := agent.NewServer(sup, buf, token)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("penguin-agent: listening on %s", *addr)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("penguin-agent: %v", err)
	}
}
