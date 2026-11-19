// mongodb-proxy is PID 1 of the `mongodb` container. It starts and stops one mongod/mongos child
// on request of the automation agent. See pkg/mongodbproxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/mongodb/mongodb-kubernetes/pkg/mongodbproxy"
)

func main() {
	listen := flag.String("listen", "unix:///var/run/mongodb-proxy/proxy.sock", "address of the process API: unix:///absolute/path.sock (mode 0600)")
	binDir := flag.String("bin-dir", "", "directory with mongod/mongos; PATH is used when empty")
	debug := flag.Bool("debug", false, "log the status requests too")
	paths := mongodbproxy.DefaultPathPolicy()
	flag.Func("config-dir", "comma-separated directories that the config file of a request has to be in", func(v string) error {
		dirs, err := parseDirs(v)
		if err != nil {
			return err
		}
		paths.ConfigDirs = dirs
		return nil
	})
	flag.Func("file-dir", "comma-separated directories that the Kerberos keytab and the LDAP CA file of a request have to be in", func(v string) error {
		dirs, err := parseDirs(v)
		if err != nil {
			return err
		}
		paths.FileDirs = dirs
		return nil
	})
	flag.Parse()

	log := newLogger(*debug)
	defer func() { _ = log.Sync() }()

	if err := run(log, *listen, *binDir, paths); err != nil {
		log.Errorw("proxy failed", "error", err)
		_ = log.Sync()
		os.Exit(1)
	}
}

func run(log *zap.SugaredLogger, listen, binDir string, paths mongodbproxy.PathPolicy) error {
	if logPath := os.Getenv("MDB_LOG_FILE_MONGODB"); logPath != "" {
		stopTail := followLog(log, logPath)
		defer stopTail()
	}

	listener, err := mongodbproxy.Listen(listen)
	if err != nil {
		return err
	}
	// Serve closes the listener, which also removes the socket file. This covers the early returns before it.
	defer func() { _ = listener.Close() }()

	sup := mongodbproxy.NewSupervisor(binDir, paths, os.Environ(), log)

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

// followLog copies the mongod log to stdout while the proxy runs. mongod writes its log to a file, and a container
// log has only stdout and stderr. tail -F reopens the file by name when mongod renames it at start and on rotation,
// and waits while the file does not exist. -n0 skips the lines of an earlier run.
func followLog(log *zap.SugaredLogger, path string) (stop func()) {
	tail := exec.Command("tail", "-F", "-n0", path)
	tail.Stdout, tail.Stderr = os.Stdout, os.Stderr
	if err := tail.Start(); err != nil {
		log.Warnw("the mongod log is not copied to stdout", "path", path, "error", err)
		return func() {}
	}
	return func() {
		// tail exits on SIGTERM without a last read. One second covers its polling interval on a network file
		// system, so the last lines of mongod are copied first. Best effort: nothing confirms the read.
		time.Sleep(time.Second)
		_ = tail.Process.Signal(syscall.SIGTERM)
		_ = tail.Wait()
	}
}

// parseDirs parses a comma-separated list of absolute directories. Setting a flag replaces the default.
func parseDirs(v string) ([]string, error) {
	var dirs []string
	for d := range strings.SplitSeq(v, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) || filepath.Clean(d) != d || d == "/" {
			return nil, fmt.Errorf("%q must be a clean absolute path other than /", d)
		}
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return nil, errors.New("at least one directory is needed")
	}
	return dirs, nil
}

func newLogger(debug bool) *zap.SugaredLogger {
	level := zapcore.InfoLevel
	if debug {
		level = zapcore.DebugLevel
	}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(os.Stderr), level)
	return zap.New(core, zap.Fields(zap.String("component", "proxy"))).Sugar()
}
