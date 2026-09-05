// Command wtcontent downloads single files out of the War Thunder content
// torrent into a sparse game directory.
//
//	wtcontent list [pattern]     list the files the torrent holds
//	wtcontent get <pattern>...   download the files that match
//
// A pattern is a path.Match pattern against the game relative path, such as
// "content*/*/res/locations_maps.dxp.bin".
//
// It reads wtcontent.json from the working directory when that file is there.
// The defaults put the game files in gameContent/files, which is a directory
// that wtcontent.OpenInstall accepts.
//
// The torrent library pulls in a cgo SQLite that this command does not use.
// Build with "-tags nosqlite,noboltdb" or with CGO_ENABLED=0 to leave it out.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/maxsupermanhd/lac/v2"
	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wtcontent list [pattern] | wtcontent get <pattern>...")
		os.Exit(2)
	}
	cfg, err := lac.FromFileJSON("wtcontent.json")
	if err != nil {
		cfg = lac.NewConf()
	}
	cont := wtcontent.NewContentTorrenter(cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	go cont.Worker(ctx)

	switch os.Args[1] {
	case "list":
		for _, n := range matching(ctx, cont, os.Args[2:]) {
			fmt.Println(n)
		}
	case "get":
		get(ctx, cont, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "wtcontent: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

// get downloads the files that match the patterns.
func get(ctx context.Context, cont *wtcontent.ContentTorrenter, patterns []string) {
	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "wtcontent: get needs at least one pattern")
		os.Exit(2)
	}
	names := matching(ctx, cont, patterns)
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "wtcontent: the torrent holds no file that matches %v\n", patterns)
		os.Exit(1)
	}
	cont.Require(names)
	if !showProgress(ctx, cont) {
		os.Exit(1)
	}
}

// matching waits for the torrent, then returns the names that match one of the
// patterns. With no pattern it returns every name.
func matching(ctx context.Context, cont *wtcontent.ContentTorrenter, patterns []string) []string {
	if !showProgress(ctx, cont) {
		os.Exit(1)
	}
	if len(patterns) == 0 {
		return cont.Files()
	}
	return wtcontent.MatchNames(cont.Files(), patterns)
}

// showProgress waits for the torrenter and redraws the status line once a
// second. The timer refreshes the display only. The wait itself sleeps until
// the worker reports a change.
func showProgress(ctx context.Context, cont *wtcontent.ContentTorrenter) bool {
	ready := make(chan bool, 1)
	go func() { ready <- cont.WaitReady(ctx) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case ok := <-ready:
			fmt.Fprintf(os.Stderr, "\r%-70s\n", cont.Status())
			return ok
		case <-tick.C:
			fmt.Fprintf(os.Stderr, "\r%-70s", cont.Status())
		}
	}
}
