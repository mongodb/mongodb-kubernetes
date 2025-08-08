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
          - tmux select-pane -t 0.0 -T '{{.ShortName}} (mdb)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/mdb/mdb.json --destDir /data/logs/mdb-debug/mdb -C=5 | tee -a /data/logs/mdb-debug/mdb/mdb.log"
      - shell_command:
          - tmux select-pane -t 0.1 -T '{{.ShortName}} (sts)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/sts/sts.json --destDir /data/logs/mdb-debug/sts -C=5 | tee -a /data/logs/mdb-debug/sts/sts.log"
      - shell_command:
          - tmux select-pane -t 0.2 -T '{{.ShortName}} (pod)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/pod/pod.json --destDir /data/logs/mdb-debug/pod -C=5 | tee -a /data/logs/mdb-debug/pod/pod.log"
      - shell_command:
          - tmux select-pane -t 0.3 -T '{{.ShortName}} (ac)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/ac/cluster-config.json --destDir /data/logs/mdb-debug/ac -C=5 | tee -a /data/logs/mdb-debug/ac/ac.log"
      - shell_command:
          - tmux select-pane -t 0.4 -T '{{.ShortName}} (ac tmp)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/ac_tmp/cluster-config.json --destDir /data/logs/mdb-debug/ac_tmp -C=5 | tee -a /data/logs/mdb-debug/ac_tmp/ac_tmp.log"
      - shell_command:
          - tmux select-pane -t 0.6 -T 'readiness.log'
          - retry_cmd "less --follow-name -R +F /data/logs/mdb-debug/readiness/readiness.log.json | tee -a /data/logs/mdb-debug/readiness/terminal_output.log"
      - shell_command:
          - tmux select-pane -t 0.5 -T '{{.ShortName}} (health)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/health/health.json --destDir /data/logs/mdb-debug/health -C=5 --ignore LastMongoUpTime | tee -a /data/logs/mdb-debug/health/health.log"
      - shell_command:
          - tmux select-pane -t 0.6 -T '{{.ShortName}} (sh status)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/sh/status.json --destDir /data/logs/mdb-debug/sh/ -C=5 | tee -a /data/logs/mdb-debug/sh/sh_status.log"
  - window_name: logs{{.PodIdx}}
    layout: tiled
    panes:
      - shell_command:
          - tmux select-pane -t 1.0 -T 'pod log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/pod.log"
      - shell_command:
          - tmux select-pane -t 1.1 -T 'automation-agent-verbose.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/automation-agent-verbose.log"
      - shell_command:
          - tmux select-pane -t 1.2 -T 'automation-agent-stderr.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/automation-agent-stderr.log"
      - shell_command:
          - tmux select-pane -t 1.3 -T 'mongodb.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/mongodb.log"
      - shell_command:
          - tmux select-pane -t 1.4 -T 'automation-agent.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/automation-agent.log"
      - shell_command:
          - tmux select-pane -t 1.5 -T 'backup-agent.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/backup-agent.log"
      - shell_command:
          - tmux select-pane -t 1.6 -T 'monitoring-agent.log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/monitoring-agent.log"
