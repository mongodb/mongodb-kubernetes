function toggleCollapse(element) {
    element.classList.toggle('collapsed');
}

function toggleSamples(id) {
    const element = document.getElementById(id);
    const button = event.target;
    if (element.style.display === 'none') {
        element.style.display = 'block';
        button.textContent = button.textContent.replace('▼', '▲').replace('Show', 'Hide');
    } else {
        element.style.display = 'none';
        button.textContent = button.textContent.replace('▲', '▼').replace('Hide', 'Show');
    }
}

function toggleExpand(element) {
    const parent = element.closest('.sample-error');
    const short = element.closest('.error-message');
    const full = parent.querySelector('.error-message-full');

    if (full.style.display === 'none') {
        full.style.display = 'block';
        short.style.display = 'none';
    } else {
        full.style.display = 'none';
        short.style.display = 'block';
    }
}

function togglePodFiles(id) {
    const row = document.getElementById(id);
    row.style.display = row.style.display === 'none' ? 'table-row' : 'none';
}

function expandTimelineMsg(element) {
    const short = element.closest('.timeline-message');
    const full = short.nextElementSibling;
    if (full && full.classList.contains('timeline-message-full')) {
        if (full.style.display === 'none') {
            full.style.display = 'block';
            short.style.display = 'none';
        } else {
            full.style.display = 'none';
            short.style.display = 'block';
        }
    }
}

function toggleTestError(id) {
    const fullError = document.getElementById(id);
    const shortError = fullError.previousElementSibling;
    const expandLink = shortError.querySelector('.expand-link');

    if (fullError.style.display === 'none') {
        fullError.style.display = 'block';
        shortError.style.display = 'none';
    } else {
        fullError.style.display = 'none';
        shortError.style.display = 'block';
    }
}

// Log viewer modal
const _logTails = JSON.parse(document.getElementById('log-tails-data').textContent);

function _escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function _highlightLogLine(escapedLine) {
    return escapedLine
        .replace(/\b(ERROR|FATAL|PANIC)\b/g, '<span class="log-error">$1</span>')
        .replace(/\b(WARN|WARNING)\b/g, '<span class="log-warn">$1</span>')
        .replace(/\b(INFO)\b/g, '<span class="log-info">$1</span>')
        .replace(/\b(DEBUG|TRACE)\b/g, '<span class="log-debug">$1</span>')
        .replace(/(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[^\s<]*)/g, '<span class="log-timestamp">$1</span>');
}

function openLogModal(filename) {
    const entry = _logTails[filename];
    if (!entry) {
        alert('Log content not available for: ' + filename);
        return;
    }

    const modal = document.getElementById('log-modal');
    const title = document.getElementById('log-modal-title');
    const info = document.getElementById('log-modal-info');
    const body = document.getElementById('log-modal-body');

    // Synthetic per-item keys are formatted as "<source_file>#<resource_name>".
    // For those, show just the resource name; otherwise fall back to the
    // trailing chunk of the filename.
    let shortName;
    if (filename.includes('#')) {
        shortName = filename.split('#').slice(-1)[0];
    } else {
        shortName = filename.includes('_') ? filename.split('_').slice(-1)[0] || filename : filename;
    }
    title.textContent = shortName;
    title.title = filename;

    if (entry.truncated) {
        const dir = entry.mode === 'head' ? 'first' : 'last';
        info.textContent = dir + ' ' + entry.shown_lines + ' of ' + entry.total_lines + ' lines';
    } else {
        info.textContent = entry.total_lines + ' lines';
    }

    // Build line-numbered, syntax-highlighted content
    const lines = entry.content.split('\n');
    const startNum = (entry.truncated && entry.mode === 'tail')
        ? entry.total_lines - entry.shown_lines + 1
        : 1;

    const htmlLines = lines.map((line, i) => {
        const num = startNum + i;
        const escaped = _escapeHtml(line);
        const highlighted = _highlightLogLine(escaped);
        return '<span class="log-line"><span class="line-num">' + num + '</span><span class="line-content">' + highlighted + '</span></span>';
    });

    body.innerHTML = htmlLines.join('');
    modal.style.display = 'flex';
    document.body.style.overflow = 'hidden';

    // Logs: scroll to bottom (most recent). Structured files: scroll to top.
    requestAnimationFrame(() => {
        body.scrollTop = entry.mode === 'head' ? 0 : body.scrollHeight;
    });
}

function closeLogModal() {
    document.getElementById('log-modal').style.display = 'none';
    document.body.style.overflow = '';
}

// Tab switching
function switchTab(slug) {
    document.querySelectorAll('.tab-btn').forEach(btn => btn.classList.remove('active'));
    document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
    const btn = document.querySelector('.tab-btn[data-slug="' + slug + '"]');
    const panel = document.getElementById('tab-' + slug);
    if (btn) btn.classList.add('active');
    if (panel) panel.classList.add('active');
}

document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeLogModal();

    // Tab navigation with [ and ] — only when modal is closed
    const modal = document.getElementById('log-modal');
    if (modal && modal.style.display !== 'none') return;

    if (e.key === '[' || e.key === ']') {
        const tabs = Array.from(document.querySelectorAll('.tab-btn'));
        if (tabs.length === 0) return;
        const activeIdx = tabs.findIndex(t => t.classList.contains('active'));
        let next;
        if (e.key === ']') {
            next = activeIdx < tabs.length - 1 ? activeIdx + 1 : 0;
        } else {
            next = activeIdx > 0 ? activeIdx - 1 : tabs.length - 1;
        }
        const slug = tabs[next].getAttribute('data-slug');
        if (slug) switchTab(slug);
    }
});

// Scroll the inline Test Output preview to the bottom — matches the modal
// behavior so the most recent pytest activity is visible without a manual scroll.
function _scrollTestOutputToBottom(preview) {
    if (preview) preview.scrollTop = preview.scrollHeight;
}

window.addEventListener('load', () => {
    document.querySelectorAll('details.test-output').forEach(d => {
        const preview = d.querySelector('.test-output-preview');
        // Scroll once now (works if open), and again on every toggle to open.
        _scrollTestOutputToBottom(preview);
        d.addEventListener('toggle', () => {
            if (d.open) _scrollTestOutputToBottom(preview);
        });
    });
    console.log('Test summary loaded.');
});
