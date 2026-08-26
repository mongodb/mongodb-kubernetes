package operator

import (
	"strconv"

	coordinationv1 "k8s.io/api/coordination/v1"
)

// The election container is the native coordination.k8s.io/Lease: one Lease per cluster per
// deployment, named after the CR, in the CR's namespace. The native spec has no term field, so
// the term rides in an annotation — the whole object is still covered by the API server's CAS,
// and the record stays kubectl-visible. The annotation key is a frozen cross-track contract
// with the installer's RBAC matrix; never change it alone.
const leadershipTermAnnotation = "operator.mongodb.com/leadership-term"

// leaseTerm reads the leadership term off a Lease. An absent or malformed annotation reads as
// (0, false): a term-less lease (hand-written, or from a foreign writer) contributes no term to
// the max-observed-term candidacy math rather than poisoning it.
func leaseTerm(lease *coordinationv1.Lease) (int64, bool) {
	raw, ok := lease.Annotations[leadershipTermAnnotation]
	if !ok {
		return 0, false
	}
	term, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return term, true
}

// setLeaseTerm stamps the leadership term, preserving any other annotations on the object.
func setLeaseTerm(lease *coordinationv1.Lease, term int64) {
	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	lease.Annotations[leadershipTermAnnotation] = strconv.FormatInt(term, 10)
}
