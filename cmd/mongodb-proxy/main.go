// mongodb-proxy is PID 1 of the `mongodb` container. It starts and stops one mongod/mongos child
// on request of the automation agent. See pkg/mongodbproxy.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/mongodb/mongodb-kubernetes/pkg/mongodbproxy"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address of the process API: host:port on a loopback address, or unix:///absolute/path.sock for a Unix domain socket (mode 0600)")
	binDir := flag.String("bin-dir", "", "directory with mongod/mongos; PATH is used when empty")
	debug := flag.Bool("debug", false, "log the status requests too")
	tailLog := flag.Bool("tail-mongod-log", true, "copy the log file of mongod/mongos to the stdout of the proxy")
	flag.Parse()

	log := newLogger(*debug)
	defer func() { _ = log.Sync() }()

	if err := run(log, *listen, *binDir, *tailLog); err != nil {
		log.Errorw("proxy failed", "error", err)
		_ = log.Sync()
		os.Exit(1)
	}
}

func run(log *zap.SugaredLogger, listen, binDir string, tailLog bool) error {
	listener, err := mongodbproxy.Listen(listen)
	if err != nil {
		return err
	}
	// Serve closes the listener, which also removes the socket file. This covers the early returns before it.
	defer func() { _ = listener.Close() }()

	var opts []mongodbproxy.Option
	if binDir != "" {
		opts = append(opts, mongodbproxy.WithBinDir(binDir))
	}
	opts = append(opts, mongodbproxy.WithEnv(os.Environ()), mongodbproxy.WithLogger(log))
	opts = append(opts, mongodbproxy.WithLogTail(tailLog))
	sup := mongodbproxy.NewSupervisor(opts...)

	srv := &http.Server{
		Handler:           mongodbproxy.NewHandler(sup, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Infow("process API listening", "address", listen)
		if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Infow("termination signal received, stopping the child")
	if err := sup.Terminate(context.Background()); err != nil {
		log.Warnw("terminating the child", "error", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(debug bool) *zap.SugaredLogger {
	level := zapcore.InfoLevel
	if debug {
		level = zapcore.DebugLevel
	}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(os.Stdout), level)
	return zap.New(core, zap.Fields(zap.String("component", "proxy"))).Sugar()
}
