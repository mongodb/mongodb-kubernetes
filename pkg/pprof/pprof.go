package pprof

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type Runnable struct {
	port int
	log  *zap.SugaredLogger
}

func NewRunnable(port int, log *zap.SugaredLogger) *Runnable {
	return &Runnable{
		port: port,
		log:  log,
	}
}

func (p *Runnable) Start(ctx context.Context) error {
	pprofAddress := fmt.Sprintf("localhost:%d", p.port)

	handler := http.NewServeMux()
	handler.HandleFunc("GET /debug/pprof/", pprof.Index)
	handler.HandleFunc("GET /debug/pprof/cmdline/", pprof.Cmdline)
	handler.HandleFunc("GET /debug/pprof/profile/", pprof.Profile)
	handler.HandleFunc("GET /debug/pprof/symbol/", pprof.Symbol)
	handler.HandleFunc("GET /debug/pprof/trace/", pprof.Trace)

	server := &http.Server{
		Addr:              pprofAddress,
		ReadHeaderTimeout: 10 * time.Second,
		Handler:           handler,
	}

	go func() {
		p.log.Infof("Starting pprof server at %s", pprofAddress)
		if err := server.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				p.log.Errorf("unable to start pprof server: %s", err.Error())
			}
		}
		p.log.Info("pprof server stopped")
	}()

	go func() {
		<-ctx.Done()
		p.log.Info("Stopping pprof server")
		if err := server.Shutdown(context.Background()); err != nil {
			p.log.Errorf("unable to shutdown pprof server: %s", err.Error())
		}
	}()

	return nil
}

// IsPprofEnabled checks if pprof is enabled based on the PPROF_ENABLED
// and OPERATOR_ENV environment variables. It returns true if:
// - PPROF_ENABLED is set to true
// - OPERATOR_ENV is set to dev or local and PPROF_ENABLED is not set
// Otherwise, it returns false.
func IsPprofEnabled(pprofEnabledString string, operatorEnv util.OperatorEnvironment) (bool, error) {
	if pprofEnabledString != "" {
		pprofEnabled, err := strconv.ParseBool(pprofEnabledString)
		if err != nil {
			return false, fmt.Errorf("unable to parse %s environment variable: %w", util.PprofEnabledEnv, err)
		}

		return pprofEnabled, nil
	}

	return operatorEnv == util.OperatorEnvironmentDev || operatorEnv == util.OperatorEnvironmentLocal, nil
}
