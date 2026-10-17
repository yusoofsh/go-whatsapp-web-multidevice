package main

import (
	"fmt"
	"os"
)

func main() {
	dir := defaultEventDir()
	if len(os.Args) == 2 {
		dir = os.Args[1]
	} else if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: check-event-goroutine-context [event-dir]")
		os.Exit(2)
	}

	hits, err := scan(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "context check: %v\n", err)
		os.Exit(2)
	}
	for _, h := range hits {
		fmt.Printf("%s:%d: context.Background() inside goroutine\n", h.path, h.line)
	}
	fmt.Printf("HITS %d\n", len(hits))
	if len(hits) > 0 {
		os.Exit(1)
	}
}

func defaultEventDir() string {
	candidates := []string{
		"infrastructure/whatsapp",
		"src/infrastructure/whatsapp",
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return "infrastructure/whatsapp"
}
