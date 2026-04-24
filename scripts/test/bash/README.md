# Bash integration tests

Real-world integration tests for shell scripts in this repo. No mocks — every
test runs the actual scripts against a real filesystem and a real git repo.

## Layout

```
scripts/test/bash/
├── README.md
├── run.sh                          # discovery + bats invocation
└── <suite>/                        # one directory per suite
    └── test_<suite>.bats
```

One subdirectory per logical area being tested (e.g. `worktree/`,
`switch_context/`, …). The runner discovers every `*.bats` file under this
tree, so adding a new suite is just creating a new subdirectory and dropping a
`*.bats` file into it.

## Running

```bash
make test-bash                              # all suites
make test-bash suite=worktree               # narrow to one suite directory
scripts/test/bash/run.sh worktree           # same, direct
scripts/test/bash/run.sh path/to/x.bats     # single file
```

`bats-core` is required:

```bash
brew install bats-core            # macOS
apt-get install bats              # Linux
```

The runner exits with a clear install message if `bats` is not on `PATH`.

## CI integration

The suite runs as the `bash_tests` task in the `unit_tests` evergreen
variant — same task group as `lint_repo`, `unit_tests_golang`, etc. Adding
a new suite is just dropping a `*.bats` file under
`scripts/test/bash/<area>/`; the runner picks it up automatically and the
`bash_tests` task runs every PR.

The suite is intentionally **not** wired into `pre-commit` so the local
hook stays fast — run it explicitly via `make test-bash` when you want to
exercise the suite before pushing.

## How tests are constructed

Suites use plain bats-core syntax — no helper libraries (`bats-assert`,
`bats-file`, etc.) — to keep the bar low for new suites.

**Conventions used in `worktree/test_worktree.bats` (copy as a template):**

- `setup()` computes `PROJECT_DIR` and `MAIN_REPO`. `PROJECT_DIR` is
  `git rev-parse --show-toplevel`; `MAIN_REPO` is the original clone (different
  from `PROJECT_DIR` when running from a linked worktree). Use `MAIN_REPO` for
  anything that lives in the shared `.git/` (hooks, refs).
- Test resources (branches, files, dirs) are suffixed with `$$` (the bats PID)
  so concurrent runs don't collide.
- `teardown()` removes anything `setup()` created. Use `|| true` and `2>/dev/null`
  so teardown never masks the real test failure.
- Use `run` for any command whose status/output you assert on. Plain calls
  (`git -C … worktree add …`) are fine for setup steps you expect to succeed —
  bats fails the test on a non-zero exit there too.
- Assertions are plain shell: `[ "$status" -eq 0 ]`, `[[ -f "$path" ]]`,
  `[[ "$output" == *"substring"* ]]`.
- For tests that depend on something potentially missing in the environment
  (e.g. an unmerged script in the main clone), gate them with a `skip` — see
  `require_init_in_main_repo` in the worktree suite.

## Debugging

Layered, in order of usefulness:

**1. Show output of `run` calls:**

```bash
bats --verbose-run scripts/test/bash/worktree/test_worktree.bats
bats --print-output-on-failure scripts/test/bash/worktree/test_worktree.bats
```

Without these flags bats swallows the captured output of passing tests.

**2. Trace the test itself:**

```bash
bats -x scripts/test/bash/worktree/test_worktree.bats        # set -x in test code
```

**3. Trace the scripts under test.** The dev scripts (`init_worktree.sh`,
`create_worktree.sh`, `recreate_python_venv.sh`) honor `MDB_BASH_DEBUG`:

```bash
MDB_BASH_DEBUG=1 bats scripts/test/bash/worktree/test_worktree.bats
```

**4. Live diagnostic prints from inside a test.** `echo` from `@test` is
swallowed unless the test fails. Write to fd 3 instead:

```bash
@test "..." {
    echo "DEBUG: about to run X" >&3
    run some_command
    echo "DEBUG: status=$status output=$output" >&3
}
```

**5. Inspect leftover state.** If `teardown()` runs cleanly there is nothing
left to inspect. Add `--no-tempdir-cleanup` to keep bats' tempdirs, or comment
out the `teardown` line you suspect is wiping evidence and rerun. Remember to
restore.

**Combine for maximum noise:**

```bash
MDB_BASH_DEBUG=1 bats -x --verbose-run --show-output-of-passing-tests \
    scripts/test/bash/worktree/test_worktree.bats
```

## Common failure modes

- **`init_worktree.sh: No such file or directory`** when running hook tests
  from a linked worktree: the `post-checkout` hook delegates to
  `${main_repo_root}/scripts/dev/init_worktree.sh`, which doesn't exist if the
  main clone is on a branch that predates this script. Tests that need it
  call `require_init_in_main_repo` and `skip` themselves; if you hit it
  outside a guarded test, run from the main clone instead.
- **`Not a directory` errors involving `.git/hooks/...`**: the test is
  treating `.git` as a directory, but in a linked worktree it's a file.
  Resolve hook paths via `MAIN_REPO`, not `PROJECT_DIR`.
- **`realpath: ...: No such file or directory`**: macOS `realpath` requires
  the path to exist. Don't use `realpath` to canonicalize a worktree path
  before creating it — compose it from a parent that already exists
  (`PARENT_DIR="$(cd "${PROJECT_DIR}/.." && pwd)"`).
- **Test passes locally but fails in pre-commit**: pre-commit runs from a
  clean state and uses `pass_filenames: false` for this hook. Make sure the
  test doesn't depend on `$1`/`$@` being passed in.
