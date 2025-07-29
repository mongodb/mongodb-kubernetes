session_name: mdb-debug
global_options:
  history-limit: 50000
  mouse: on
  pane-border-status: top
  pane-border-format: "#{pane_index}: #{pane_title}"
window_options:
  remain-on-exit: on
shell_command_before:
  - |
    function retry_cmd() {
      local cmd="$1"
      local delay="${2:-3}"
      
      while true; do
        eval "$cmd"
        echo "Retrying..."
        sleep "$delay"
      done
    }
windows:
  - window_name: json
    layout: even-horizontal
    panes:
      - shell_command:
        - tmux select-pane -t 0.0 -T '{{.ShortName}} (pod)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/pod/pod.json --destDir /data/logs/mdb-debug/pod -C=5 | tee -a /data/logs/mdb-debug/pod/pod.log"
      - shell_command:
        - tmux select-pane -t 0.1 -T '{{.ShortName}} (sts)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/sts/sts.json --destDir /data/logs/mdb-debug/sts -C=5 | tee -a /data/logs/mdb-debug/sts/sts.log"
      - shell_command:
        - tmux select-pane -t 0.2 -T '{{.ShortName}} (pod log)'
        - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/pod.log | tee -a /data/logs/mdb-debug/logs/pod_viewer.log"
  - window_name: logs{{.PodIdx}}
    layout: even-horizontal
    panes:
      - shell_command:
        - tmux select-pane -t 1.0 -T 'daemon-startup.log'
        - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/daemon-startup.log | tee -a /data/logs/mdb-debug/logs/daemon-startup_viewer.log"
      - shell_command:
        - tmux select-pane -t 1.1 -T 'daemon.log'
        - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/daemon.log | tee -a /data/logs/mdb-debug/logs/daemon_viewer.log"
