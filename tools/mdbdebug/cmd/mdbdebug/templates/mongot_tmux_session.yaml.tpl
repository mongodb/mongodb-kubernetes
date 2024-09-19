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
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/cr/cr.json --destDir {{.BaseLogDir}}/mdb-debug/cr -C=5"
      - shell_command:
        - tmux select-pane -t 0.1 -T '{{.ShortName}} (sts)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/sts/sts.json --destDir {{.BaseLogDir}}/mdb-debug/sts -C=3"
      - shell_command:
        - tmux select-pane -t 0.2 -T '{{.ShortName}} (mongot cfg)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/config/config.json --destDir {{.BaseLogDir}}/mdb-debug/config -C=3"
      - shell_command:
        - tmux select-pane -t 0.3 -T '{{.ShortName}} (pod)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/pod/pod.json --destDir {{.BaseLogDir}}/mdb-debug/pod -C=3"
      - shell_command:
        - tmux select-pane -t 0.4 -T '{{.ShortName}} (pod log)'
        - retry_cmd "lnav {{.BaseLogDir}}/container.log"
