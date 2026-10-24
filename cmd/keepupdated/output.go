package main

import (
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// Some formatters, including FlagSet help, discard write errors.
type outputWriter struct {
	io.Writer
	failed atomic.Bool
}

func (w *outputWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err != nil {
		w.failed.Store(true)
	}
	return n, err
}

func run(args []string, out, errOut io.Writer) int {
	// MCP transport shutdown follows the SDK's protocol, not CLI output rules.
	if len(args) > 0 && (args[0] == "mcp" && len(args) == 1 || args[0] == "mcp-worker") {
		return runCommand(args, out, errOut)
	}
	// Let ordinary CLI writes return EPIPE instead of exiting before error handling.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	output := &outputWriter{Writer: out}
	diagnostics := &outputWriter{Writer: errOut}
	code := runCommand(args, output, diagnostics)
	// Apply owns its result-aware output failure code.
	if (output.failed.Load() || diagnostics.failed.Load()) && (len(args) == 0 || args[0] != "apply") {
		return 2
	}
	return code
}
