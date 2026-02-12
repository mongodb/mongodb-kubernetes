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

function copyJSON() {
    const jsonData = document.getElementById('test-data').textContent;
    navigator.clipboard.writeText(jsonData).then(() => {
        alert('JSON data copied to clipboard!');
    }).catch(err => {
        console.error('Failed to copy:', err);
    });
}

function downloadJSON() {
    const jsonData = document.getElementById('test-data').textContent;
    const blob = new Blob([jsonData], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'test-summary.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
}

// Log viewer modal
const _logTails = JSON.parse(document.getElementById('log-tails-data').textContent);

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

    // Show just the short filename in the title
    const shortName = filename.includes('_') ? filename.split('_').slice(-1)[0] || filename : filename;
    title.textContent = shortName;
    title.title = filename;

    if (entry.truncated) {
        const dir = entry.mode === 'head' ? 'first' : 'last';
        info.textContent = dir + ' ' + entry.shown_lines + ' of ' + entry.total_lines + ' lines';
    } else {
        info.textContent = entry.total_lines + ' lines';
    }

    body.textContent = entry.content;
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

// Auto-collapse long sections on load
window.addEventListener('load', () => {
    console.log('Test summary loaded. JSON data available in #test-data element.');
});
