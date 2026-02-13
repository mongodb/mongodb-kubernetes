"""HTML rendering for E2E test summary dashboard.

Builds section HTML from collected data and assembles the final single-file
HTML document with inlined CSS/JS from dashboard.css and dashboard.js.
"""

import json
from pathlib import Path
from html import escape
from typing import Dict, Any
from datetime import datetime, timezone
from collections import defaultdict

# Load CSS and JS assets once from sibling files
_ASSETS_DIR = Path(__file__).parent
_CSS = (_ASSETS_DIR / 'dashboard.css').read_text()
_JS = (_ASSETS_DIR / 'dashboard.js').read_text()


def _format_age(timestamp_str: str, reference: datetime = None) -> str:
    """Format a creation timestamp as a human-readable age like k9s.

    Returns strings like '18s', '3m', '25m', '2h', '1d', '45d'.
    """
    if not timestamp_str:
        return ''
    try:
        ts = str(timestamp_str).replace('Z', '+00:00')
        dt = datetime.fromisoformat(ts)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        ref = reference or datetime.now(timezone.utc)
        delta = ref - dt
        seconds = int(delta.total_seconds())
        if seconds < 0:
            return '0s'
        if seconds < 60:
            return f'{seconds}s'
        minutes = seconds // 60
        if minutes < 60:
            return f'{minutes}m'
        hours = seconds // 3600
        if hours < 24:
            return f'{hours}h'
        days = seconds // 86400
        return f'{days}d'
    except (ValueError, TypeError):
        return str(timestamp_str)[:19]


def _format_size(size_bytes: int) -> str:
    """Format byte size for display."""
    if size_bytes == 0:
        return '0B'
    elif size_bytes < 1024:
        return f'{size_bytes}B'
    elif size_bytes < 1024 * 1024:
        return f'{size_bytes / 1024:.1f}KB'
    else:
        return f'{size_bytes / 1024 / 1024:.1f}MB'


def _render_timeline_entry(idx: int, entry: Dict) -> str:
    """Render a single timeline entry."""
    severity = entry.get('severity', '')
    event_type = entry.get('event_type', '')
    reason = entry.get('reason', '')
    resource = entry.get('resource', '')
    message = entry.get('message', '')
    timestamp = entry.get('timestamp')
    count = entry.get('count', 1)

    # Format time as HH:MM:SS for display, full timestamp on hover
    time_display = ''
    if timestamp:
        time_display = f"<span class='timeline-timestamp' title='{escape(str(timestamp))}'>{escape(str(timestamp)[11:19])}</span>"

    # Severity badge
    badge_class = ''
    if severity == 'warning' or event_type == 'Warning':
        badge_class = 'badge-warning'
    elif severity in ('error', 'critical'):
        badge_class = 'badge-error'

    # Reason tag
    reason_html = ''
    if reason:
        reason_html = f"<span class='event-reason'>{escape(reason)}</span>"

    # Count indicator
    count_html = ''
    if count and count > 1:
        count_html = f"<span class='event-count'>x{count}</span>"

    # Message with expand for long content
    if len(message) > 200:
        msg_html = f"<div class='timeline-message'>{escape(message[:200])}...<span class='expand-link' onclick='expandTimelineMsg(this)'>expand</span></div>"
        msg_html += f"<div class='timeline-message-full' style='display:none;'>{escape(message)}</div>"
    else:
        msg_html = f"<div class='timeline-message'>{escape(message)}</div>"

    return f"""<div class='timeline-entry {severity} {badge_class}'>
        <div class='timeline-number'>{idx + 1}</div>
        <div class='timeline-content'>
            <div class='timeline-header'>
                {time_display}
                {reason_html}
                {count_html}
                <span class='timeline-resource'>{escape(str(resource))}</span>
            </div>
            {msg_html}
        </div>
    </div>"""


def _render_diagnostics(data: Dict, log_tails: Dict) -> str:
    """Render diagnostics section above tabs."""
    diagnostics = data.get('diagnostics', {})
    if not diagnostics:
        return ''

    html = "<div class='diagnostics-section'>"
    html += "<h3>Diagnostics</h3>"
    html += "<div class='diag-links'>"
    for label in sorted(diagnostics.keys()):
        diag = diagnostics[label]
        fname = diag['file']
        # Show cluster name only (hide namespace)
        display = diag['cluster']
        if fname in log_tails:
            html += f"<code class='diag-link' onclick=\"openLogModal('{escape(fname)}')\" title='{escape(fname)}'>{escape(display)}</code>"
        else:
            html += f"<code class='file-tag' title='{escape(fname)}'>{escape(display)}</code>"
    html += "</div></div>"
    return html


def _render_pods_enhanced(pods: list, log_tails: Dict, multi_cluster: bool, reference_time: datetime = None) -> str:
    """Render enhanced pod table with describe button."""
    if not pods:
        return "<p>No pods found</p>"

    by_cluster = defaultdict(list)
    for pod in pods:
        by_cluster[pod.get('cluster', 'default')].append(pod)

    html = ''
    global_idx = 0
    for cluster in sorted(by_cluster.keys()):
        cluster_pods = by_cluster[cluster]
        if multi_cluster:
            html += f"<div class='cluster-header'>{escape(cluster)}</div>"

        html += "<table><tr><th>#</th><th>Name</th><th>Phase</th><th>Ready</th><th>Restarts</th><th>Age</th><th>Files</th><th>Describe</th></tr>"
        for idx, pod in enumerate(cluster_pods[:50], 1):
            global_idx += 1
            phase_class = 'healthy' if pod['phase'] == 'Running' else 'unhealthy'
            file_count = (len(pod['files'].get('logs', []))
                          + len(pod['files'].get('agent_logs', []))
                          + len(pod['files'].get('config', []))
                          + (1 if pod['files'].get('describe') else 0)
                          + (1 if pod['files'].get('health') else 0))

            html += f"<tr class='{phase_class}' onclick='togglePodFiles(\"pod-files-{global_idx}\")' style='cursor: pointer;'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(pod['name'])}</td>"
            html += f"<td><span class='badge badge-{phase_class}'>{escape(pod['phase'])}</span></td>"
            html += f"<td>{escape(pod['ready'])}</td>"
            html += f"<td>{pod['restarts']}</td>"
            age = _format_age(pod.get('created', ''), reference_time)
            html += f"<td title='{escape(str(pod.get('created', '')))}'>{escape(age)}</td>"
            html += f"<td>{file_count}</td>"

            # Describe button
            describe_file = pod['files'].get('describe')
            if describe_file and describe_file in log_tails:
                html += f"<td><span class='describe-btn' onclick=\"event.stopPropagation(); openLogModal('{escape(describe_file)}')\">describe</span></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"

            # Expandable file details row
            all_files = (pod['files'].get('logs', []) + pod['files'].get('agent_logs', [])
                         + pod['files'].get('config', []))
            if pod['files'].get('describe'):
                all_files.append(pod['files']['describe'])
            if pod['files'].get('health'):
                all_files.append(pod['files']['health'])
            html += f"<tr id='pod-files-{global_idx}' class='pod-files-row' style='display: none;'>"
            html += "<td colspan='8'>"

            # Container sub-table
            containers = pod.get('containers', [])
            if containers:
                html += "<table class='container-table'><tr><th>Name</th><th>State</th><th>Ready</th><th>Restarts</th><th>Exit Code</th><th>Last State</th></tr>"
                for c in containers:
                    state = c.get('state', 'unknown')
                    state_class = f'state-{state}' if state in ('running', 'waiting', 'terminated') else ''
                    ready = c.get('ready', False)
                    ready_html = "<span class='ready-yes'>&#10003;</span>" if ready else "<span class='ready-no'>&#10007;</span>"
                    restarts = c.get('restarts', 0)
                    exit_code = c.get('exit_code')
                    exit_html = f"<span class='exit-code'>{exit_code}</span>" if exit_code is not None and exit_code != 0 else '-'
                    last_state = c.get('last_state')
                    last_state_html = escape(last_state) if last_state else '-'
                    html += f"<tr><td class='resource-name'>{escape(c.get('name', ''))}</td>"
                    html += f"<td><span class='badge {state_class}'>{escape(state)}</span></td>"
                    html += f"<td>{ready_html}</td>"
                    html += f"<td>{restarts}</td>"
                    html += f"<td>{exit_html}</td>"
                    html += f"<td>{last_state_html}</td></tr>"
                html += "</table>"

            html += "<div class='pod-files-detail'>"
            if all_files:
                for f in sorted(all_files):
                    file_label = f.split('_')[-1] if '_' in f else f
                    if f in log_tails:
                        html += f"<code class='file-tag-link' onclick=\"event.stopPropagation(); openLogModal('{escape(f)}')\">{escape(file_label)}</code> "
                    else:
                        html += f"<code class='file-tag'>{escape(file_label)}</code> "
            else:
                html += "<span class='truncation-note'>No files found for this pod</span>"
            html += "</div></td></tr>"

        if len(cluster_pods) > 50:
            html += f"<tr><td colspan='8' class='truncation-note'>Showing first 50 of {len(cluster_pods)} pods.</td></tr>"
        html += "</table>"

    return html


def _render_statefulsets_enhanced(statefulsets: list, log_tails: Dict, multi_cluster: bool, reference_time: datetime = None) -> str:
    """Render enhanced StatefulSet table."""
    if not statefulsets:
        return "<p>No StatefulSets found</p>"

    by_cluster = defaultdict(list)
    for sts in statefulsets:
        by_cluster[sts.get('cluster', 'default')].append(sts)

    html = ''
    for cluster in sorted(by_cluster.keys()):
        cluster_items = by_cluster[cluster]
        if multi_cluster:
            html += f"<div class='cluster-header'>{escape(cluster)}</div>"

        html += "<table><tr><th>#</th><th>Name</th><th>Replicas</th><th>Owner</th><th>Age</th><th>YAML</th></tr>"
        for idx, sts in enumerate(cluster_items[:50], 1):
            readiness_class = 'healthy' if sts['ready_replicas'] == sts['desired_replicas'] else 'unhealthy'
            yaml_file = sts.get('files', {}).get('yaml', '')
            age = _format_age(sts.get('created', ''), reference_time)
            html += f"<tr class='{readiness_class}'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(sts['name'])}</td>"
            html += f"<td>{sts['ready_replicas']}/{sts['desired_replicas']}</td>"
            html += f"<td class='resource-name'>{escape(sts.get('owner_ref', '-') or '-')}</td>"
            html += f"<td title='{escape(str(sts.get('created', '')))}'>{escape(age)}</td>"
            if yaml_file and yaml_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"
        html += "</table>"

    return html


def _render_deployments_enhanced(deployments: list, log_tails: Dict, multi_cluster: bool, reference_time: datetime = None) -> str:
    """Render enhanced Deployment table."""
    if not deployments:
        return "<p>No Deployments found</p>"

    by_cluster = defaultdict(list)
    for deploy in deployments:
        by_cluster[deploy.get('cluster', 'default')].append(deploy)

    html = ''
    for cluster in sorted(by_cluster.keys()):
        cluster_items = by_cluster[cluster]
        if multi_cluster:
            html += f"<div class='cluster-header'>{escape(cluster)}</div>"

        html += "<table><tr><th>#</th><th>Name</th><th>Replicas</th><th>Age</th><th>YAML</th></tr>"
        for idx, deploy in enumerate(cluster_items[:50], 1):
            readiness_class = 'healthy' if deploy['ready_replicas'] == deploy['desired_replicas'] else 'unhealthy'
            yaml_file = deploy.get('files', {}).get('yaml', '')
            age = _format_age(deploy.get('created', ''), reference_time)
            html += f"<tr class='{readiness_class}'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(deploy['name'])}</td>"
            html += f"<td>{deploy['ready_replicas']}/{deploy['desired_replicas']}</td>"
            html += f"<td title='{escape(str(deploy.get('created', '')))}'>{escape(age)}</td>"
            if yaml_file and yaml_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"
        html += "</table>"

    return html


def _render_generic_table(items: list, source_files: list, log_tails: Dict, multi_cluster: bool, reference_time: datetime = None) -> str:
    """Render a generic resource table for any resource type."""
    if not items and not source_files:
        return "<p>No resources found</p>"

    # If no parsed items but source_files exist, show view buttons
    if not items:
        html = "<div style='display: flex; flex-wrap: wrap; gap: 6px;'>"
        for sf in source_files:
            fname = sf['name']
            label = sf['cluster'] + '/' + sf['namespace'] if multi_cluster else sf['namespace']
            if fname in log_tails:
                html += f"<code class='file-tag-link' onclick=\"openLogModal('{escape(fname)}')\" title='{escape(fname)}'>View ({escape(label)})</code>"
            else:
                html += f"<code class='file-tag' title='{escape(fname)}'>{escape(label)}</code>"
        html += "</div>"
        return html

    # Group items by cluster
    by_cluster = defaultdict(list)
    for item in items:
        by_cluster[item.get('cluster', 'default')].append(item)

    html = ''
    for cluster in sorted(by_cluster.keys()):
        cluster_items = by_cluster[cluster]
        if multi_cluster:
            html += f"<div class='cluster-header'>{escape(cluster)}</div>"

        html += "<table><tr><th>#</th><th>Name</th><th>Namespace</th>"
        if cluster_items and cluster_items[0].get('kind'):
            html += "<th>Kind</th>"
        html += "<th>Age</th><th>Source</th></tr>"

        for idx, item in enumerate(cluster_items[:100], 1):
            html += "<tr>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(item.get('name', ''))}</td>"
            html += f"<td>{escape(item.get('namespace', ''))}</td>"
            if item.get('kind'):
                html += f"<td>{escape(item.get('kind', ''))}</td>"
            created = item.get('created', '')
            age = _format_age(created, reference_time)
            html += f"<td title='{escape(str(created))}'>{escape(age)}</td>"
            source_file = item.get('source_file', '')
            if source_file and source_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(source_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"

        if len(cluster_items) > 100:
            html += f"<tr><td colspan='6' class='truncation-note'>Showing first 100 of {len(cluster_items)} items.</td></tr>"
        html += "</table>"

    return html


def _render_secrets_table(items: list, log_tails: Dict, multi_cluster: bool) -> str:
    """Render secrets table (name + view link)."""
    if not items:
        return "<p>No secrets found</p>"

    by_cluster = defaultdict(list)
    for item in items:
        by_cluster[item.get('cluster', 'default')].append(item)

    html = ''
    for cluster in sorted(by_cluster.keys()):
        cluster_items = by_cluster[cluster]
        if multi_cluster:
            html += f"<div class='cluster-header'>{escape(cluster)}</div>"

        html += "<table><tr><th>#</th><th>Secret Name</th><th>Namespace</th><th>View</th></tr>"
        for idx, item in enumerate(cluster_items[:100], 1):
            html += "<tr>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(item.get('name', ''))}</td>"
            html += f"<td>{escape(item.get('namespace', ''))}</td>"
            source_file = item.get('source_file', '')
            if source_file and source_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(source_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"
        html += "</table>"

    return html


def _render_resource_tabs(data: Dict, log_tails: Dict, reference_time: datetime) -> str:
    """Render the tabbed resource browser."""
    generic = data['resources'].get('generic', {})
    if not generic:
        return ''

    multi_cluster = data['topology']['type'] == 'multi-cluster'

    # Priority ordering for tabs
    priority_order = [
        'pods', 'statefulsets', 'deployments', 'services', 'configmaps',
        'secrets', 'persistent_volume_claims',
    ]

    # Build ordered slug list
    all_slugs = list(generic.keys())
    ordered_slugs = []
    for slug in priority_order:
        if slug in all_slugs:
            ordered_slugs.append(slug)
    # Add remaining slugs alphabetically
    for slug in sorted(all_slugs):
        if slug not in ordered_slugs:
            ordered_slugs.append(slug)

    if not ordered_slugs:
        return ''

    # Tab bar
    html = "<h2>Resource Browser</h2>"
    html += "<div class='resource-tabs'>"
    html += "<div class='tab-bar'>"
    default_tab = 'pods' if 'pods' in ordered_slugs else ordered_slugs[0]
    for i, slug in enumerate(ordered_slugs):
        info = generic[slug]
        count = len(info['items'])
        active = ' active' if slug == default_tab else ''
        display = escape(info['display_name'])
        html += f"<button class='tab-btn{active}' data-slug='{escape(slug)}' onclick=\"switchTab('{escape(slug)}')\">"
        html += f"{display}<span class='tab-count'>{count}</span></button>"
    html += "</div>"

    # Tab panels
    for i, slug in enumerate(ordered_slugs):
        info = generic[slug]
        active = ' active' if slug == default_tab else ''
        html += f"<div id='tab-{escape(slug)}' class='tab-panel{active}'>"

        # Enhanced renderers for pods/statefulsets/deployments
        if slug == 'pods' and data['resources']['pods']:
            html += _render_pods_enhanced(data['resources']['pods'], log_tails, multi_cluster, reference_time)
        elif slug == 'statefulsets' and data['resources']['statefulsets']:
            html += _render_statefulsets_enhanced(data['resources']['statefulsets'], log_tails, multi_cluster, reference_time)
        elif slug == 'deployments' and data['resources']['deployments']:
            html += _render_deployments_enhanced(data['resources']['deployments'], log_tails, multi_cluster, reference_time)
        elif slug == 'secrets':
            html += _render_secrets_table(info['items'], log_tails, multi_cluster)
        else:
            html += _render_generic_table(info['items'], info['source_files'], log_tails, multi_cluster, reference_time)

        html += "</div>"

    html += "</div>"
    return html


def _render_artifacts_by_cluster(data: Dict) -> str:
    """Render artifacts as a compact cluster summary table.

    The per-pod file details are already in the Resource Inventory section.
    This section is a high-level index: what data do we have, per cluster.
    """
    by_cluster = data['artifacts'].get('by_cluster', {})
    if not by_cluster:
        return '<p>No artifacts found</p>'

    # Build per-cluster type counts, skipping 'default' noise
    cluster_stats = []
    type_columns = set()
    for cluster in sorted(by_cluster.keys()):
        namespaces = by_cluster[cluster]
        for ns in sorted(namespaces.keys()):
            files = namespaces[ns]
            label = f"{cluster}/{ns}" if cluster != 'default' else ns

            by_type = defaultdict(lambda: {'count': 0, 'size': 0})
            for f in files:
                by_type[f['type']]['count'] += 1
                by_type[f['type']]['size'] += f['size_bytes']

            type_columns.update(by_type.keys())
            total_size = sum(f['size_bytes'] for f in files)
            cluster_stats.append({
                'label': label,
                'total': len(files),
                'total_size': total_size,
                'by_type': dict(by_type),
            })

    if not cluster_stats:
        return '<p>No cluster artifacts found</p>'

    # Render as a compact table
    col_order = ['log', 'agent_log', 'describe', 'resource_dump', 'events',
                 'health', 'config', 'diagnostics', 'json', 'other']
    cols = [c for c in col_order if c in type_columns]
    cols += sorted(type_columns - set(col_order))

    col_labels = {
        'log': 'Logs', 'describe': 'Describe', 'resource_dump': 'Resources',
        'events': 'Events', 'health': 'Health', 'config': 'Config',
        'diagnostics': 'Diag', 'json': 'JSON', 'yaml': 'YAML',
        'other': 'Other', 'test_results': 'Tests',
    }

    html = "<table class='artifacts-table'><tr><th>Cluster</th><th>Files</th><th>Size</th>"
    for col in cols:
        html += f"<th>{escape(col_labels.get(col, col))}</th>"
    html += "</tr>"

    for stat in cluster_stats:
        html += f"<tr><td class='resource-name'>{escape(stat['label'])}</td>"
        html += f"<td>{stat['total']}</td>"
        html += f"<td>{_format_size(stat['total_size'])}</td>"
        for col in cols:
            info = stat['by_type'].get(col)
            if info:
                html += f"<td>{info['count']}</td>"
            else:
                html += "<td class='empty-cell'>-</td>"
        html += "</tr>"

    html += "</table>"

    return html


def _render_failed_tests(data: Dict) -> str:
    """Render failed test details with expandable error messages."""
    failed_tests = data['test_run'].get('tests', {}).get('failed', 0)
    if failed_tests == 0:
        return ""

    html = "<h2>Failed Tests</h2><div class='section'>"
    for idx, test in enumerate(data['test_run'].get('tests', {}).get('details', [])):
        if test['status'] == 'failed':
            html += "<div class='failed-test'>"
            html += f"<div class='failed-test-header'>"
            html += f"<span class='test-name'><strong>{escape(test['name'])}</strong></span>"
            html += f"<span class='test-duration'>{test['duration']:.1f}s</span>"
            html += "</div>"
            error_msg = test.get('error_message', 'Unknown error')
            if len(error_msg) > 200:
                html += f"<div class='test-error'>{escape(error_msg[:200])}..."
                html += f"<span class='expand-link' onclick='toggleTestError(\"test-error-{idx}\")'>expand</span></div>"
                html += f"<div id='test-error-{idx}' class='test-error-full' style='display: none;'>{escape(error_msg)}</div>"
            else:
                html += f"<div class='test-error'>{escape(error_msg)}</div>"
            if test.get('file'):
                html += f"<div class='test-location'>{escape(test['file'])}"
                if test.get('line'):
                    html += f":{escape(test['line'])}"
                html += "</div>"
            html += "</div>"
    html += "</div>"
    return html


def _render_resource_inventory(data: Dict, log_tails: Dict, reference_time: datetime) -> str:
    """Render resource inventory as diagnostics section + tabbed resource browser."""
    diagnostics_html = _render_diagnostics(data, log_tails)
    tabs_html = _render_resource_tabs(data, log_tails, reference_time)
    return diagnostics_html + tabs_html


def _render_error_catalog(data: Dict) -> str:
    """Render error catalog with expandable sample errors."""
    top_errors = data['error_patterns'][:5]

    html = "<h2>Error Catalog</h2><div class='section'>"
    if top_errors:
        for idx, pattern in enumerate(top_errors):
            severity_class = pattern['severity']
            html += f"<div class='error-pattern {severity_class}'>"
            html += f"<h4>{escape(pattern['description'])} ({pattern['count']} occurrences)</h4>"
            html += f"<p><strong>Pattern:</strong> <code>{escape(pattern['pattern'])}</code></p>"
            html += f"<p><strong>Affected resources:</strong> {len(pattern['affected_resources'])}</p>"

            # Show sample errors
            sample_error_ids = pattern.get('error_ids', [])[:3]
            if sample_error_ids:
                html += f"<button class='toggle-btn' onclick='toggleSamples(\"samples-{idx}\")'>Show sample errors \u25bc</button>"
                html += f"<div id='samples-{idx}' class='sample-errors' style='display: none;'>"
                for error_id in sample_error_ids:
                    error = next((e for e in data['errors'] if e['id'] == error_id), None)
                    if error:
                        html += "<div class='sample-error'>"
                        html += f"<div class='sample-error-header'>"
                        html += f"<span class='error-source'>{escape(error['source'].get('file', 'unknown'))}:{error['source'].get('line', '?')}</span>"
                        if error.get('timestamp'):
                            html += f"<span class='error-time'>{escape(error['timestamp'])}</span>"
                        html += "</div>"
                        message = error.get('message', '')
                        if len(message) > 150:
                            html += f"<div class='error-message'>{escape(message[:150])}..."
                            html += f"<span class='expand-link' onclick='toggleExpand(this)'>expand</span></div>"
                            html += f"<div class='error-message-full' style='display: none;'>{escape(message)}</div>"
                        else:
                            html += f"<div class='error-message'>{escape(message)}</div>"
                        html += "</div>"
                html += "</div>"

            html += "</div>"
    else:
        html += "<p>No errors detected</p>"
    html += "</div>"
    return html


def generate_html(data: Dict[str, Any], test_name: str, variant: str, status: str) -> str:
    """Generate interactive HTML with embedded JSON."""
    # Parse reference time for age calculations (use generation time, not viewing time)
    reference_time = datetime.fromisoformat(data['meta']['generated_at'].replace('Z', '+00:00'))

    # Extract log tails before serializing — keep the copyable JSON clean
    log_tails = data.pop('log_tails', {})
    log_tails_json = json.dumps(log_tails)

    json_data = json.dumps(data, indent=2)
    escaped_json = escape(json_data)

    # Calculate quick stats
    failed_tests = data['test_run'].get('tests', {}).get('failed', 0)
    total_tests = data['test_run'].get('tests', {}).get('total', 0)

    unhealthy_pods = sum(1 for pod in data['resources']['pods']
                         if pod.get('phase') != 'Running' or pod.get('restarts', 0) > 0)

    status_color = "#28a745" if status == "PASSED" else "#dc3545" if status == "FAILED" else "#6c757d"

    # Build sections
    failed_tests_html = _render_failed_tests(data)
    error_catalog_html = _render_error_catalog(data)
    resource_html = _render_resource_inventory(data, log_tails, reference_time)
    timeline_html = ''.join([_render_timeline_entry(idx, entry) for idx, entry in enumerate(data['timeline'][:200])])
    timeline_truncation = f"<p class='truncation-note'>Showing first 200 of {len(data['timeline'])} events.</p>" if len(data['timeline']) > 200 else ''

    html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>E2E Test Summary - {escape(test_name)}</title>
    <style>
{_CSS}    </style>
</head>
<body>
    <div class="container">
        <h1>E2E Test Summary</h1>

        <div class="metadata">
            <div class="metadata-item">
                <span class="metadata-label">Status</span>
                <span class="metadata-value"><span class="status-badge" style="--status-color: {status_color}">{status}</span></span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Test Name</span>
                <span class="metadata-value">{escape(test_name)}</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Variant</span>
                <span class="metadata-value">{escape(variant)}</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Duration</span>
                <span class="metadata-value">{int(data['test_run'].get('duration_seconds', 0)) // 60}m {int(data['test_run'].get('duration_seconds', 0)) % 60}s</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Tests</span>
                <span class="metadata-value">{total_tests} total ({failed_tests} failed)</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Generated</span>
                <span class="metadata-value">{data['meta']['generated_at'][:19]}</span>
            </div>
        </div>

        {failed_tests_html}

        {resource_html}

        <div class="quick-diagnosis">
            <h3>Quick Diagnosis</h3>
            <ul>
                <li><strong>Test Status:</strong> {status} ({failed_tests}/{total_tests} tests failed)</li>
                <li><strong>Unhealthy Pods:</strong> {unhealthy_pods}</li>
                <li><strong>Total Errors:</strong> {len(data['errors'])} errors detected across {len(data['error_patterns'])} patterns</li>
                <li><strong>Topology:</strong> {data['topology']['type']}</li>
            </ul>
        </div>

        {error_catalog_html}

        <h2 class="collapsible collapsed" onclick="toggleCollapse(this)">Timeline ({len(data['timeline'])} events)</h2>
        <div class="collapsible-content section">
            <div style="max-height: 600px; overflow-y: auto;">
                {timeline_html}
            </div>
            {timeline_truncation}
        </div>

        <h2 class="collapsible collapsed" onclick="toggleCollapse(this)">Artifacts ({data['artifacts']['summary']['total_files']} files, {data['artifacts']['summary']['total_size_bytes'] / 1024 / 1024:.1f} MB)</h2>
        <div class="collapsible-content section">
            <p><strong>By type:</strong> {', '.join([f"{k}: {v}" for k, v in sorted(data['artifacts']['summary']['by_type'].items())])}</p>
            {_render_artifacts_by_cluster(data)}
        </div>

        <button class="copy-button" onclick="copyJSON()">\U0001f4cb Copy JSON Data</button>
        <button class="copy-button" onclick="downloadJSON()">\U0001f4be Download JSON</button>
        <h2 class="collapsible collapsed" onclick="toggleCollapse(this)">Full JSON Data</h2>
        <div class="collapsible-content section">
            <pre id="json-display">{escaped_json}</pre>
        </div>
    </div>

    <!-- Log viewer modal -->
    <div id="log-modal" class="log-modal" style="display:none;" onclick="if(event.target===this)closeLogModal()">
        <div class="log-modal-content">
            <div class="log-modal-header">
                <span id="log-modal-title" class="log-modal-title"></span>
                <span id="log-modal-info" class="log-modal-info"></span>
                <button class="log-modal-close" onclick="closeLogModal()">&times;</button>
            </div>
            <pre id="log-modal-body" class="log-modal-body"></pre>
        </div>
    </div>

    <script id="test-data" type="application/json">
{json_data}
    </script>

    <script id="log-tails-data" type="application/json">
{log_tails_json}
    </script>

    <script>
{_JS}
    </script>
</body>
</html>"""

    return html
