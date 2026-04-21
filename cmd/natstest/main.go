package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

func main() {
	url   := flag.String("nats-url", "nats://127.0.0.1:4222", "NATS URL")
	creds := flag.String("nats-creds", "", "Path to .creds file")
	flag.Parse()

	opts := []nats.Option{nats.Timeout(5 * time.Second)}
	if *creds != "" {
		opts = append(opts, nats.UserCredentials(*creds))
		fmt.Printf("Using creds: %s\n", *creds)
	}

	fmt.Printf("Connecting to %s ...\n", *url)
	nc, err := nats.Connect(*url, opts...)
	if err != nil {
		log.Fatalf("FAIL: %v", err)
	}
	defer nc.Close()

	fmt.Printf("OK — connected, server=%s\n", nc.ConnectedServerName())
}
