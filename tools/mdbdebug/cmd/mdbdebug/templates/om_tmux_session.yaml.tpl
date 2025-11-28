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
    layout: tiled
    panes:
      - shell_command:
        - tmux select-pane -t 0.0 -T '{{.ShortName}} (CR)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/om/om.json --destDir /data/logs/mdb-debug/om -C=5 | tee -a /data/logs/mdb-debug/om/om.log"
      - shell_command:
        - tmux select-pane -t 0.1 -T '{{.ShortName}} (pod)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/pod/pod.json --destDir /data/logs/mdb-debug/pod -C=5 | tee -a /data/logs/mdb-debug/pod/pod.log"
      - shell_command:
        - tmux select-pane -t 0.2 -T '{{.ShortName}} (sts)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/sts/sts.json --destDir /data/logs/mdb-debug/sts -C=5 | tee -a /data/logs/mdb-debug/sts/sts.log"
      - shell_command:
        - tmux select-pane -t 0.3 -T '{{.ShortName}} (mms-conf)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/pod_conf/conf-mms.json --destDir /data/logs/mdb-debug/pod_conf -A=20 -B=20 | tee -a /data/logs/mdb-debug/pod_conf/conf-mms.log"
      - shell_command:
        - tmux select-pane -t 0.4 -T '{{.ShortName}} (pod log)'
        - retry_cmd "lnav /data/logs/mdb-debug/logs/pod.log"
      - shell_command:
        - tmux select-pane -t 0.5 -T '{{.ShortName}} (state)'
        - retry_cmd "diffwatch --file /data/logs/mdb-debug/state/state.json --destDir /data/logs/mdb-debug/state | tee -a /data/logs/mdb-debug/state/state.log"
  - window_name: logs{{.PodIdx}}
    layout: tiled
    panes:
      - shell_command:
        - tmux select-pane -t 1.0 -T 'mms-migration.log'
        - retry_cmd "lnav /data/logs/mdb-debug/logs/mms-migration.log"
      - shell_command:
        - tmux select-pane -t 1.1 -T 'mms0-access.log'
        - retry_cmd "lnav /data/logs/mdb-debug/logs/mms0-access.log"
      - shell_command:
        - tmux select-pane -t 1.2 -T 'mms0-startup.log'
        - retry_cmd "lnav /data/logs/mdb-debug/logs/mms0-startup.log"
      - shell_command:
        - tmux select-pane -t 1.3 -T 'mms0.log'
        - retry_cmd "lnav /data/logs/mdb-debug/logs/mms0.log"
