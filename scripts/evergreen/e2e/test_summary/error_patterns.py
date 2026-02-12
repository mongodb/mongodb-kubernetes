"""Error pattern definitions for E2E test log analysis."""

import re
from typing import List, Tuple

# Error patterns for classification
ERROR_PATTERNS = {
    'crash_loop': {
        'regex': r'CrashLoopBackOff',
        'severity': 'critical',
        'description': 'Container repeatedly crashing'
    },
    'oom_killed': {
        'regex': r'OOMKilled',
        'severity': 'critical',
        'description': 'Out of memory - container killed'
    },
    'image_pull_error': {
        'regex': r'(ImagePullBackOff|ErrImagePull)',
        'severity': 'critical',
        'description': 'Failed to pull container image'
    },
    'failed_scheduling': {
        'regex': r'FailedScheduling',
        'severity': 'critical',
        'description': 'Pod could not be scheduled'
    },
    'failed_mount': {
        'regex': r'FailedMount',
        'severity': 'critical',
        'description': 'Volume mount failed'
    },
    'evicted': {
        'regex': r'Evicted',
        'severity': 'critical',
        'description': 'Pod evicted due to resource pressure'
    },
    'connection_refused': {
        'regex': r'connection refused',
        'severity': 'error',
        'description': 'Connection refused'
    },
    'connection_timeout': {
        'regex': r'(connection timeout|i/o timeout)',
        'severity': 'error',
        'description': 'Connection timeout'
    },
    'auth_failed': {
        'regex': r'(authentication failed|unauthorized)',
        'severity': 'error',
        'description': 'Authentication failed'
    },
    'cert_error': {
        'regex': r'(certificate|x509|TLS handshake)',
        'severity': 'error',
        'description': 'Certificate or TLS error'
    },
    'readiness_probe': {
        'regex': r'Readiness probe failed',
        'severity': 'warning',
        'description': 'Readiness probe failed'
    },
    'liveness_probe': {
        'regex': r'Liveness probe failed',
        'severity': 'warning',
        'description': 'Liveness probe failed'
    },
    'admission_denied': {
        'regex': r'admission webhook.*denied',
        'severity': 'error',
        'description': 'Admission webhook denied request'
    },
    'reconciler_error': {
        'regex': r'Reconciler error',
        'severity': 'error',
        'description': 'Controller reconciliation error'
    },
}


def get_compiled_patterns() -> List[Tuple[str, "re.Pattern[str]", dict]]:
    """Return pre-compiled patterns as (name, compiled_regex, info) tuples.

    Pre-compiles regexes once instead of per-line in _scan_log_for_errors.
    """
    return [
        (name, re.compile(info['regex'], re.IGNORECASE), info)
        for name, info in ERROR_PATTERNS.items()
    ]
