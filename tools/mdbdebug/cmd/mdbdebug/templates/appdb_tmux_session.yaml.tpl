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
        eval "${cmd}"
        echo "Retrying... ${cmd}"
        sleep "${delay}"
      done
    }
windows:
  - window_name: json
    layout: tiled
    panes:
      - shell_command:
        - tmux select-pane -t 0.0 -T '{{.ShortName}} (CR)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/cr/cr.json --destDir {{.BaseLogDir}}/mdb-debug/cr -C=5 | tee -a {{.BaseLogDir}}/mdb-debug/cr/cr.log"
      - shell_command:
        - tmux select-pane -t 0.1 -T '{{.ShortName}} (ac)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/ac/cluster-config.json --destDir {{.BaseLogDir}}/mdb-debug/ac -C=3 | tee -a {{.BaseLogDir}}/mdb-debug/ac/ac.log"
      - shell_command:
        - tmux select-pane -t 0.2 -T '{{.ShortName}} (sts)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/sts/sts.json --destDir {{.BaseLogDir}}/mdb-debug/sts -C=3 | tee -a {{.BaseLogDir}}/mdb-debug/sts/sts.log"
      - shell_command:
        - tmux select-pane -t 0.3 -T '{{.ShortName}} (health)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/health/health.json --destDir {{.BaseLogDir}}/mdb-debug/health -C=3 --ignore LastMongoUpTime | tee -a {{.BaseLogDir}}/mdb-debug/health/health.log"
      - shell_command:
        - tmux select-pane -t 0.4 -T '{{.ShortName}} (pod)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/pod/pod.json --destDir {{.BaseLogDir}}/mdb-debug/pod -C=3 | tee -a {{.BaseLogDir}}/mdb-debug/pod/pod.log"
      - shell_command:
        - tmux select-pane -t 0.5 -T 'readiness.log'
        - retry_cmd "lnav {{.BaseLogDir}}/mdb-debug/readiness/readiness.log.json"
      - shell_command:
        - tmux select-pane -t 0.6 -T '{{.ShortName}} (state)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/state/state.json --destDir {{.BaseLogDir}}/mdb-debug/state | tee -a {{.BaseLogDir}}/mdb-debug/state/state.log"
      - shell_command:
        - tmux select-pane -t 0.7 -T '{{.ShortName}} (mongod cfg)'
        - retry_cmd "diffwatch --file {{.BaseLogDir}}/mdb-debug/mongod_config/config.json --destDir {{.BaseLogDir}}/mdb-debug/mongod_config/ -C=5 | tee -a {{.BaseLogDir}}/mdb-debug/mongod_config/mongod_config.log"
  - window_name: logs{{.PodIdx}}
    layout: tiled
    panes:
      - shell_command:
        - tmux select-pane -t 1.0 -T 'automation-agent.log'
        - retry_cmd "lnav {{.BaseLogDir}}/automation-agent.log"
      - shell_command:
        - tmux select-pane -t 1.1 -T 'automation-agent-verbose.log'
        - retry_cmd "lnav {{.BaseLogDir}}/automation-agent-verbose.log"
      - shell_command:
        - tmux select-pane -t 1.2 -T 'mongodb.log'
        - retry_cmd "lnav {{.BaseLogDir}}/mongodb.log"
      - shell_command:
        - tmux select-pane -t 1.3 -T 'readiness.log'
        - retry_cmd "lnav {{.BaseLogDir}}/readiness.log"
      - shell_command:
        - tmux select-pane -t 1.4 -T 'mongod_container.log'
        - retry_cmd "lnav {{.BaseLogDir}}/mongod_container.log"
      - shell_command:
        - tmux select-pane -t 1.5 -T 'agent_container.log'
        - retry_cmd "lnav {{.BaseLogDir}}/agent_container.log"
