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
          - tmux select-pane -t 0.5 -T 'readiness.log'
          - retry_cmd "less --follow-name -R +F /data/logs/mdb-debug/readiness/readiness.log.json | tee -a /data/logs/mdb-debug/readiness/terminal_output.log"
      - shell_command:
          - tmux select-pane -t 0.6 -T '{{.ShortName}} (rs config)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/rs/config.json --destDir /data/logs/mdb-debug/rs/ -C=5 | tee -a /data/logs/mdb-debug/rs/rs_config.log"
      - shell_command:
          - tmux select-pane -t 0.7 -T '{{.ShortName}} (rs hello)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/rs_hello/hello.json --destDir /data/logs/mdb-debug/rs_hello/ -C=5 --ignore connectionId --ignore '\\d{19}' --ignore '\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}' | tee -a /data/logs/mdb-debug/rs_hello/rs_hello.log"
      - shell_command:
          - tmux select-pane -t 0.8 -T '{{.ShortName}} (mongod cfg)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/mongod_config/config.json --destDir /data/logs/mdb-debug/mongod_config/ -C=5 | tee -a /data/logs/mdb-debug/mongod_config/mongod_config.log"
      - shell_command:
          - tmux select-pane -t 0.9 -T '{{.ShortName}} (health)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/health/health.json --destDir /data/logs/mdb-debug/health -C=5 --ignore LastMongoUpTime | tee -a /data/logs/mdb-debug/health/health.log"
      - shell_command:
          - tmux select-pane -t 0.10 -T '{{.ShortName}} (state)'
          - retry_cmd "diffwatch --file /data/logs/mdb-debug/state/state.json --destDir /data/logs/mdb-debug/state | tee -a /data/logs/mdb-debug/state/state.log"
  - window_name: logs{{.PodIdx}}
    layout: tiled
    panes:
      - shell_command:
          - tmux select-pane -t 1.0 -T 'pod log'
          - retry_cmd "less --follow-name +F /data/logs/mdb-debug/logs/pod.log"
      - shell_command:
          - tmux select-pane -t 1.1 -T 'automation-agent-verbose.log'
          - retry_cmd "less --follow-name +F  /data/logs/automation-agent-verbose.log"
      - shell_command:
          - tmux select-pane -t 1.2 -T 'automation-agent-stderr.log'
          - retry_cmd "less --follow-name +F  /data/logs/automation-agent-stderr.log"
      - shell_command:
          - tmux select-pane -t 1.3 -T 'mongodb.log'
          - retry_cmd "less --follow-name +F  /data/logs/mongodb.log"
      - shell_command:
          - tmux select-pane -t 1.4 -T 'automation-agent.log'
          - retry_cmd "less --follow-name +F  /data/logs/automation-agent.log"
      - shell_command:
          - tmux select-pane -t 1.5 -T 'backup-agent.log'
          - retry_cmd "less --follow-name +F  /data/logs/backup-agent.log"
      - shell_command:
          - tmux select-pane -t 1.6 -T 'monitoring-agent.log'
          - retry_cmd "less --follow-name +F  /data/logs/monitoring-agent.log"
