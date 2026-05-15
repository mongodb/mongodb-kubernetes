package coordraft

import (
	"context"
	"sort"
	"sync"
	"time"

	"golang.org/x/xerrors"
)

// Scheduler is the leader-side lease allocator. One scheduler runs in the
// background per Manager; it ticks every TickInterval and, when this node is
// leader and no lease is outstanding, proposes the next lease in deterministic
// order (component → cluster).
//
// Cluster list is provided at construction (PoC simplification — see arch doc
// §6.10 for the future where the FSM's ClusterIndex map is authoritative).
// Component list defaults to ["config", "shard-0..N-1", "mongos"] but can be
// overridden via SchedulerConfig.Components.
type Scheduler struct {
	cfg           SchedulerConfig
	mgr           *Manager
	fsm           *FSM
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	leaseTTL      time.Duration
}

// SchedulerConfig configures a Scheduler.
type SchedulerConfig struct {
	// Clusters is the deterministic cluster list to allocate against. Sorted
	// lexicographically inside NewScheduler.
	Clusters []string
	// Components is the ordered list of components a sharded cluster has.
	// E.g. ["config", "shard-0", "shard-1", "mongos"].
	Components []string
	// TickInterval is how often tick() runs. Defaults to 100ms in tests.
	TickInterval time.Duration
	// LeaseTTL is what the scheduler asks the FSM to set when proposing a
	// lease_allocate. Defaults to 60s.
	LeaseTTL time.Duration
}

// NewScheduler constructs a scheduler. The scheduler is NOT started until
// Start is called.
func NewScheduler(mgr *Manager, fsm *FSM, cfg SchedulerConfig) (*Scheduler, error) {
	if mgr == nil || fsm == nil {
		return nil, xerrors.New("manager and fsm required")
	}
	if len(cfg.Components) == 0 {
		return nil, xerrors.New("at least one component required")
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 100 * time.Millisecond
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 60 * time.Second
	}
	clusters := append([]string(nil), cfg.Clusters...)
	sort.Strings(clusters)
	cfg.Clusters = clusters
	return &Scheduler{
		cfg:      cfg,
		mgr:      mgr,
		fsm:      fsm,
		leaseTTL: cfg.LeaseTTL,
	}, nil
}

// Start launches the scheduler goroutine. Stop the scheduler by calling
// Stop() or by cancelling the parent ctx.
func (s *Scheduler) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.cfg.TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tick()
			}
		}
	}()
}

// Stop signals the scheduler to exit and waits for it.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// tick is the scheduler's main decision: if leader, no lease, and there's a
// pending (component, cluster), propose a lease_allocate. Otherwise no-op.
func (s *Scheduler) tick() {
	if !s.mgr.IsLeader() {
		return
	}
	if s.fsm.GetActiveLease() != nil {
		return
	}
	component, cluster, ok := s.computeNextLease()
	if !ok {
		return
	}
	data, err := EncodeProposal(ProposalLeaseAllocate, LeaseAllocatePayload{
		Component:   component,
		ClusterName: cluster,
		TTL:         s.leaseTTL,
	})
	if err != nil {
		return
	}
	_ = s.mgr.Apply(data, applyTimeout).Error()
}

// computeNextLease walks components in cfg.Components order, then clusters in
// sorted cfg.Clusters order, and returns the first (component, cluster) tuple
// that is NOT already Ready in the FSM's PerClusterStatus. Returns
// (_, _, false) if everything is Ready (nothing to do).
//
// "Ready" here means: PerClusterStatus[cluster].ComponentStatus[component].Ready
// is true. If a cluster has never reported, treat it as not-ready.
func (s *Scheduler) computeNextLease() (string, string, bool) {
	state := s.fsm.GetState()
	for _, component := range s.cfg.Components {
		for _, cluster := range s.cfg.Clusters {
			cs, ok := state.PerClusterStatus[cluster]
			if !ok {
				return component, cluster, true
			}
			compStatus, ok := cs.ComponentStatus[component]
			if !ok || !compStatus.Ready {
				return component, cluster, true
			}
		}
	}
	return "", "", false
}
