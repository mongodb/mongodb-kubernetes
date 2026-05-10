# Host shell setup for the MCK dev env

Add this one-line function to your `~/.zshrc` (or `~/.bashrc`):

    mck-env() { . "$(git rev-parse --show-toplevel 2>/dev/null || echo .)/scripts/dev/devenv"; }

Then in any worktree:

    cd <worktree>
    mck-env

`mck-env` sources `.generated/context.env` (logical) and
`.generated/context.host.env` (site bytes for the host) with
`set -a`, then activates the worktree's `venv/` if present.

If `.generated/context.host.env` is missing, run `make switch` first.

Optional: prepend the worktree's `bin/` to PATH so project-installed
`kubectl`, `helm`, etc. take precedence. The devcontainer does this
via `/etc/profile.d/mck-bin.sh`; on the host you control your own PATH.
A common pattern (zsh):

    chpwd() {
      [[ -d ./bin ]] && export PATH="$(realpath ./bin):${PATH}"
    }

…or just add the absolute path of the active worktree to PATH manually.
