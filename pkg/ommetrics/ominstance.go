package ommetrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// omInstance is the connection pool, TLS settings and export concurrency bound shared by every project on one Ops Manager.
type omInstance struct {
	base      string
	client    *http.Client
	tlsOpts   TLSOptions // guarded by registry.mu
	transport atomic.Pointer[http.Transport]
	sem       chan struct{}
}

func newOMInstance(cfg Config, base string, tlsOpts TLSOptions) (*omInstance, error) {
	tlsCfg, err := tlsOpts.TLSConfig()
	if err != nil {
		return nil, err
	}
	o := &omInstance{base: base, tlsOpts: tlsOpts, sem: make(chan struct{}, cfg.MaxConcurrentPerOM)}
	o.transport.Store(newTransport(cfg, tlsCfg))
	o.client = &http.Client{
		Transport: o,
		// Never follow a redirect that could carry the agent API key to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return o, nil
}

// setTLS swaps in a new transport when tlsOpts change. Caller holds registry.mu.
func (o *omInstance) setTLS(cfg Config, tlsOpts TLSOptions) error {
	if o.tlsOpts == tlsOpts {
		return nil
	}
	tlsCfg, err := tlsOpts.TLSConfig()
	if err != nil {
		return err
	}
	old := o.transport.Swap(newTransport(cfg, tlsCfg))
	o.tlsOpts = tlsOpts
	// Projects on one OM disagreeing on TLS settings shows up as this line on every reconcile.
	zap.S().Infof("ommetrics: TLS settings for Ops Manager %s changed; applying them to all of its projects", o.base)
	closeIdleNowAndLater(cfg, old)
	return nil
}

func (o *omInstance) close(cfg Config) {
	closeIdleNowAndLater(cfg, o.transport.Load())
}

// closeIdleNowAndLater also catches connections that in-flight requests return to the pool after retirement.
func closeIdleNowAndLater(cfg Config, t *http.Transport) {
	t.CloseIdleConnections()
	time.AfterFunc(cfg.PerDestTimeout, t.CloseIdleConnections)
}

type ctxDestKey struct{}

func withDestination(ctx context.Context, d *destination) context.Context {
	return context.WithValue(ctx, ctxDestKey{}, d)
}

// RoundTrip authenticates as the destination in the request context: username = group ID, password = agent API key.
func (o *omInstance) RoundTrip(req *http.Request) (*http.Response, error) {
	d, ok := req.Context().Value(ctxDestKey{}).(*destination)
	if !ok {
		return nil, fmt.Errorf("ommetrics: request without destination context: %s", req.URL.Path)
	}
	req = req.Clone(req.Context()) // RoundTrippers must not mutate the caller's request
	req.SetBasicAuth(d.key.groupID, *d.agentAPIKey.Load())
	return o.transport.Load().RoundTrip(req)
}

func newTransport(cfg Config, tlsCfg *tls.Config) *http.Transport {
	return &http.Transport{
		// The default of 2 would drop most of the fanout's HTTP/1.1 connections after every cycle.
		MaxIdleConnsPerHost: cfg.MaxConcurrentPerOM,
		MaxIdleConns:        cfg.MaxIdleConns,
		// Outlive the collect interval so connections survive between cycles.
		IdleConnTimeout:     2 * cfg.CollectInterval,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     tlsCfg,
		Proxy:               http.ProxyFromEnvironment,
	}
}
