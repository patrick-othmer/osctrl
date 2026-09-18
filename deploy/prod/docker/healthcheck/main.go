// Command healthcheck is a tiny static binary baked into the distroless
// osctrl images. Distroless images have no shell, so compose healthchecks
// cannot use wget/curl; this binary probes the service /health endpoint
// (200 healthy, 204 degraded-DB both count as "up") and is what keeps the
// edge/frontend startup gating meaningful.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func probe(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("unexpected status %s", resp.Status)
}

func main() {
	addr := os.Getenv("HEALTHCHECK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9000"
	}
	if err := probe(addr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
