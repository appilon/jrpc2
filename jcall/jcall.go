// Program jcall issues RPC calls to a JSON-RPC server.
//
// Usage:
//    jcall [options] <address> {<method> <params>}...
//
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bitbucket.org/creachadair/jrpc2"
	"bitbucket.org/creachadair/jrpc2/channel/chanutil"
	"bitbucket.org/creachadair/jrpc2/jctx"
)

var (
	dialTimeout = flag.Duration("dial", 5*time.Second, "Timeout on dialing the server (0 for no timeout)")
	callTimeout = flag.Duration("timeout", 0, "Timeout on each call (0 for no timeout)")
	doNotify    = flag.Bool("notify", false, "Send a notification")
	withContext = flag.Bool("c", false, "Send context with request")
	chanFraming = flag.String("f", "raw", `Channel framing ("json", "line", "lsp", "raw", "varint")`)
	doSeq       = flag.Bool("seq", false, "Issue calls sequentially rather than as a batch")
	withLogging = flag.Bool("v", false, "Enable verbose logging")
	withMeta    = flag.String("meta", "", "Attach this JSON value as request metadata (implies -c)")
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: [options] %s <address> {<method> <params>}...

Connect to the specified address and transmit the specified JSON-RPC method
calls (as a batch, if more than one is provided).  The resulting response
values are printed to stdout.

Options:
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	// There must be at least one request, and more are permitted.  Each method
	// must have an argument, though it may be empty.
	if flag.NArg() < 3 || flag.NArg()%2 == 0 {
		log.Fatal("Arguments are <address> {<method> <params>}...")
	}
	nc := chanutil.Framing(*chanFraming)
	if nc == nil {
		log.Fatalf("Unknown channel framing %q", *chanFraming)
	}
	ctx := context.Background()
	if *withMeta != "" {
		mc, err := jctx.WithMetadata(ctx, json.RawMessage(*withMeta))
		if err != nil {
			log.Fatalf("Invalid request metadata: %v", err)
		}
		ctx = mc
		*withContext = true
	}

	// Connect to the server and establish a client.
	addr := flag.Arg(0)
	ntype, addr := parseAddress(addr)
	conn, err := net.DialTimeout(ntype, addr, *dialTimeout)
	if err != nil {
		log.Fatalf("Dial %q: %v", addr, err)
	}
	defer conn.Close()

	opts := new(jrpc2.ClientOptions)
	if *withContext {
		opts.EncodeContext = jctx.Encode
	}
	if *withLogging {
		opts.Logger = log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile)
	}
	cli := jrpc2.NewClient(nc(conn, conn), opts)

	if *callTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *callTimeout)
		defer cancel()
	}
	rsps, err := issueCalls(ctx, cli, flag.Args()[1:])
	if err != nil {
		log.Fatalf("Call failed: %v", err)
	}
	failed := false
	for i, rsp := range rsps {
		if rerr := rsp.Error(); rerr != nil {
			log.Printf("Error (%d): %v", i+1, rerr)
			failed = true
			continue
		}
		var result json.RawMessage
		if err := rsp.UnmarshalResult(&result); err != nil {
			log.Printf("Decoding (%d): %v", i+1, err)
			failed = true
			continue
		}
		fmt.Println(string(result))
	}
	if failed {
		os.Exit(1)
	}
}

func parseAddress(s string) (ntype, addr string) {
	// A TCP address has the form [host]:port, so there must be a colon in it.
	// If we don't find that, assume it's a unix-domain socket.
	if strings.Contains(s, ":") {
		return "tcp", s
	}
	return "unix", s
}

func issueCalls(ctx context.Context, cli *jrpc2.Client, args []string) ([]*jrpc2.Response, error) {
	if *doSeq && !*doNotify {
		return issueSequential(ctx, cli, args)
	}
	specs := make([]jrpc2.Spec, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		specs = append(specs, jrpc2.Spec{
			Method: args[i],
			Params: param(args[i+1]),
			Notify: *doNotify,
		})
	}
	batch, err := cli.Batch(ctx, specs)
	if err != nil {
		return nil, err
	}
	return batch.Wait(), nil
}

func issueSequential(ctx context.Context, cli *jrpc2.Client, args []string) ([]*jrpc2.Response, error) {
	var rsps []*jrpc2.Response
	for i := 0; i < len(args); i += 2 {
		rsp, err := cli.Call(ctx, args[i], param(args[i+1]))
		if err != nil {
			return nil, err
		}
		rsps = append(rsps, rsp)
	}
	return rsps, nil
}

func param(s string) interface{} {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}
