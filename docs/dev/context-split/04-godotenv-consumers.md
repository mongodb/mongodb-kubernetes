# Step 04 — Update godotenv consumers (operator + pytest)

## Goal

Update the two godotenv-style env loaders so they:
1. Default to the host file list, switching to the devc list when
   `[[ -f /.dockerenv ]]`.
2. Load three files in order with override semantics:
   `context.env` (logical) → `context.<side>.env` (site) →
   `context.operator.env` (logical operator overlay).
3. Warn (don't abort) on missing files.

## Preconditions

- Step 02 complete: the new file artifacts exist.
- Operator and pytest currently load only `.generated/context.operator.env`
  (operator) and `.generated/context.env` (pytest); both single-file
  loads.

This step is parallelisable with step 03 (`devenv` bootstrap). Land
both before step 05 (consumer migration).

## Files touched

- `main.go` (function `loadEnvFromLocalFileForDevelopment`,
  currently around line 541-555)
- `docker/mongodb-kubernetes-tests/tests/conftest.py` (function
  `_load_env_from_local_file_for_development`, currently around
  line 23-42)

No other Go or Python files need changes for this step.

## Detailed plan

### 1. `main.go` — update `loadEnvFromLocalFileForDevelopment`

Existing body (around lines 541-555):

```go
// loadEnvFromLocalFileForDevelopment loads env vars from .generated/context.operator.env if not running in "prod" env
func loadEnvFromLocalFileForDevelopment() {
    if getOperatorEnv() == util.OperatorEnvironmentProd {
        return
    }

    envFile := ".generated/context.operator.env"
    if _, err := os.Stat(envFile); err == nil {
        if err := godotenv.Load(envFile); err != nil {
            log.Warnf("Failed to load environment variables from file %s: %v", envFile, err)
        } else {
            log.Infof("Loaded environment variables from file %s", envFile)
        }
    }
}
```

Replace with:

```go
// loadEnvFromLocalFileForDevelopment loads dev-mode env vars from
// .generated/context.{env,<side>.env,operator.env} when not running in
// "prod" env. Side is detected by /.dockerenv: present -> devc, absent
// -> host. Files are loaded with godotenv.Overload so later files win,
// matching the layering of:
//   context.env       (logical, identical bytes regardless of side)
//   context.<side>.env (site bytes: PROJECT_DIR, KUBECONFIG, K8S_FWD_PROXY, ...)
//   context.operator.env (logical operator overlay; KUBECONFIG/KUBE_CONFIG_PATH
//                         already provided by context.<side>.env and stripped
//                         from this file by switch_context.sh)
// Each file is independently optional — missing ones log a warning.
// See docs/dev/context-split/README.md.
func loadEnvFromLocalFileForDevelopment() {
    if getOperatorEnv() == util.OperatorEnvironmentProd {
        return
    }

    side := "host"
    if _, err := os.Stat("/.dockerenv"); err == nil {
        side = "devc"
    }

    envFiles := []string{
        ".generated/context.env",
        fmt.Sprintf(".generated/context.%s.env", side),
        ".generated/context.operator.env",
    }
    for _, envFile := range envFiles {
        if _, err := os.Stat(envFile); err != nil {
            log.Warnf("Env file %s not found (run 'make switch' on the %s side); skipping.", envFile, side)
            continue
        }
        if err := godotenv.Overload(envFile); err != nil {
            log.Warnf("Failed to load environment variables from file %s: %v", envFile, err)
        } else {
            log.Infof("Loaded environment variables from file %s", envFile)
        }
    }
}
```

Notes:
- `godotenv.Overload` is the override-semantics version. `godotenv.Load`
  does NOT override existing env vars; we want override so site bytes
  layer over logical bytes correctly.
- `fmt` is already imported in `main.go`; if not, add it. Check the
  imports block at the top of `main.go`.
- The warning text mentions `make switch` so a dev grep'ing logs
  finds the fix immediately.

### 2. `conftest.py` — update `_load_env_from_local_file_for_development`

Existing body (around lines 23-42):

```python
def _load_env_from_local_file_for_development():
    """Load environment variables from .generated/context.env for local development.
    Similar to operator's loadEnvFromLocalFileForDevelopment() - only loads if
    NAMESPACE env var is not already set (i.e., not running in CI/Evergreen).
    """
    # Path relative to this file: tests/conftest.py -> ../../../.. -> repo root
    env_file = Path(__file__).joinpath("..", "..", "..", "..", ".generated", "context.env").resolve()

    if not env_file.exists():
        return

    if os.environ.get("NAMESPACE"):
        print(f"NAMESPACE already set, skipping loading environment variables from {env_file}")
        return

    load_dotenv(env_file)
    print(f"Loaded environment variables from file {env_file}")
```

Replace with:

```python
def _load_env_from_local_file_for_development():
    """Load environment variables from .generated/context.{env,<side>.env}
    for local development. Mirrors the operator's
    loadEnvFromLocalFileForDevelopment() — only loads if NAMESPACE env var
    is not already set (i.e. not running in CI/Evergreen).

    Side detection follows the same /.dockerenv rule used by
    scripts/dev/devenv and main.go's env loader. Files are loaded with
    override=True so site bytes layer over logical bytes correctly.
    See docs/dev/context-split/README.md.
    """
    if os.environ.get("NAMESPACE"):
        print("NAMESPACE already set, skipping loading environment variables from .generated/")
        return

    # Path relative to this file: tests/conftest.py -> ../../../.. -> repo root
    repo_root = Path(__file__).joinpath("..", "..", "..", "..").resolve()
    side = "devc" if Path("/.dockerenv").exists() else "host"

    env_files = [
        repo_root / ".generated" / "context.env",
        repo_root / ".generated" / f"context.{side}.env",
    ]
    for env_file in env_files:
        if not env_file.exists():
            print(
                f"WARN: env file {env_file} not found (run 'make switch' on the {side} side); skipping.",
            )
            continue
        load_dotenv(env_file, override=True)
        print(f"Loaded environment variables from file {env_file}")
```

Notes:
- `load_dotenv(env_file, override=True)` is python-dotenv's override
  flag. Default is False (won't replace existing env vars). We need
  override so site layers over logical.
- Pytest doesn't load `context.operator.env` — that's operator-only
  bytes. Tests get logical + site only.
- The early `NAMESPACE` check stays at the top so CI/Evergreen
  contexts (which set NAMESPACE in env) skip the local load entirely.
- `Path` is already imported (`from pathlib import Path` at top of file).
- `print` is intentional here (matches existing style in the file);
  pytest captures stdout/stderr from conftest setup.

### 3. Verification (Go code)

After the edit, run:

```bash
# Compile (no test changes)
cd /workspace
go build ./...
# expect: clean

# Linter
go vet ./...
# expect: clean

# Static check
test -f .pre-commit-config.yaml && pre-commit run --all-files --files main.go
```

### 4. Verification (Python code)

```bash
cd /workspace/docker/mongodb-kubernetes-tests
# Pytest collection only (won't run tests, just imports conftest)
PYTEST_OTEL_ENABLED=false python -m pytest --collect-only tests/ 2>&1 | head -20
# Expect:
#   "Loaded environment variables from file .../.generated/context.env"
#   "Loaded environment variables from file .../.generated/context.<side>.env"
#   no traceback

# With NAMESPACE pre-set (simulates CI):
NAMESPACE=ci-fake PYTEST_OTEL_ENABLED=false python -m pytest --collect-only tests/ 2>&1 | head -5
# Expect:
#   "NAMESPACE already set, skipping loading environment variables from .generated/"

# With site file missing (simulates fresh worktree):
mv .generated/context.devc.env /tmp/saved.context.devc.env
PYTEST_OTEL_ENABLED=false python -m pytest --collect-only tests/ 2>&1 | head -10
mv /tmp/saved.context.devc.env .generated/context.devc.env
# Expect: WARN line about missing file; collection still succeeds
#   (or fails downstream because OPS_MANAGER_URL etc. are unset, which
#   is expected — load itself doesn't abort).
```

### 5. Verification (operator startup smoke test)

```bash
# Inside devc, run the operator briefly and verify the log line shows
# all three files loaded.
cd /workspace
bash scripts/dev/op_run.sh --wait
tmux capture-pane -t mck-operator -p | head -40
# Expect three "Loaded environment variables from file …" lines:
#   .generated/context.env
#   .generated/context.devc.env
#   .generated/context.operator.env
tmux kill-session -t mck-operator
```

## Outputs / postconditions

- Operator logs three "Loaded environment variables…" lines on startup
  (in dev mode).
- Pytest conftest logs two such lines on test session start.
- Missing files log a single WARN line each, no abort.
- Both consumers use the same `/.dockerenv` side-detection rule.

## Pitfalls

- **`fmt` import in main.go.** Verify the import block already has
  `"fmt"`; if not, add it. Most likely already imported.
- **`godotenv.Overload` exists in `joho/godotenv` v1.4+.** Confirm the
  go.mod version supports it (likely fine — Overload has been there
  for years).
- **`load_dotenv` override semantics.** python-dotenv's behavior:
  with override=True, KEY=VALUE in the file overrides any pre-existing
  env var of the same name. Without it, the first loaded value sticks.
  We want override because we load logical first, then site (site
  overrides any KEY where logical and site disagree on the same name —
  shouldn't happen in practice, but harmless).
- **EVG CI path.** When operator runs on EVG CI under
  `OPERATOR_ENV=prod`, the function returns early (today's behavior
  preserved). The new code path doesn't execute in prod.
- **No `context.<side>.operator.env` reference.** The operator overlay
  is logical-only after step 02 (KUBECONFIG/KUBE_CONFIG_PATH stripped).
  Don't try to load a per-side operator file — there isn't one.
- **Path from conftest.** `Path(__file__).joinpath("..", "..", "..", "..").resolve()`
  goes from `docker/mongodb-kubernetes-tests/tests/conftest.py` up to
  the repo root. Verify the count: 4 `..` to go up four levels
  (tests/ -> mongodb-kubernetes-tests/ -> docker/ -> repo). Existing
  code uses the same count; preserved.

## Commit

```
operator + tests: load per-side env files with override semantics

Update main.go's loadEnvFromLocalFileForDevelopment and conftest.py's
_load_env_from_local_file_for_development to load three (operator) /
two (pytest) .env files in order with godotenv.Overload /
load_dotenv(override=True). Side is detected via /.dockerenv (devc)
vs. absent (host); the same rule used by scripts/dev/devenv and
switch_context.sh.

Each file is independently optional — missing files log a single WARN
line and continue. The early-return on NAMESPACE-already-set (CI path)
is preserved.

Step 4/6 of docs/dev/context-split.
```

## Cross-references

- Master plan: [`README.md`](README.md)
- Previous step: [`02-switch-context.md`](02-switch-context.md)
- Parallelisable with: [`03-devenv-bootstrap.md`](03-devenv-bootstrap.md)
- Next step: [`05-shell-consumers.md`](05-shell-consumers.md)

## Notes (implementation)

Implemented on `lsierant/devcontainer` as a single commit covering both
the Go and Python consumers.

### Files changed

- `main.go`: replaced `loadEnvFromLocalFileForDevelopment` body per
  section 1 of the plan. Loads three files in order (`context.env`,
  `context.<side>.env`, `context.operator.env`) via `godotenv.Overload`.
  Side detected via `os.Stat("/.dockerenv")`. Missing files log a
  warning and continue. Early-return on `OperatorEnv == prod`
  preserved. `fmt` was already in the import block — no import changes.
- `docker/mongodb-kubernetes-tests/tests/conftest.py`: replaced
  `_load_env_from_local_file_for_development` body per section 2.
  Loads two files (`context.env`, `context.<side>.env`) — no
  `context.operator.env` for pytest, by design. Side detected via
  `Path("/.dockerenv").exists()`. Uses `load_dotenv(env_file,
  override=True)`. Early `NAMESPACE` short-circuit kept at the top so
  CI/Evergreen contexts skip the local load entirely.

### Deviations from the plan

None. The plan's pseudocode dropped in cleanly for both files.

### Verification

Go side (clean):

```bash
go build ./...   # clean
go vet ./...     # clean
```

Python side (host, NAMESPACE unset, venv activated):

```
$ . venv/bin/activate && cd docker/mongodb-kubernetes-tests \
    && env -u NAMESPACE PYTEST_OTEL_ENABLED=false \
       python -m pytest --collect-only tests/ 2>&1 \
       | grep -E "Loaded environment variables from file|WARN: env file" | head
Loaded environment variables from file /Users/.../.generated/context.env
Loaded environment variables from file /Users/.../.generated/context.host.env
```

Python side (NAMESPACE pre-set, simulating CI):

```
$ NAMESPACE=ci-fake PYTEST_OTEL_ENABLED=false python -m pytest --collect-only tests/ 2>&1 \
    | grep -E "NAMESPACE already set"
NAMESPACE already set, skipping loading environment variables from .generated/
```

(Pytest collection in the NAMESPACE-set path subsequently fails inside
`kubernetes.config.load_incluster_config()` because we're on host, not
in a cluster — unrelated to env loading and out of scope for this
step.)

`pre-commit run --files main.go docker/mongodb-kubernetes-tests/tests/conftest.py`
— all hooks pass after re-creating the temporary `.export.env` stubs
documented in step 02 Notes (the shell-consumer hooks still rely on
`.generated/context.export.env` until step 05 migrates them).

### Issues uncovered for steps 05/06

- **`.export.env` stub still required for pre-commit until step 05.**
  Step 02's note documents that `update-release-json`,
  `update-values-yaml`, `generate-standalone-yaml`,
  `generate-and-update`, and `validate-helm-charts` all source
  `set_env_context.sh` → `.generated/context.export.env`. With step 02's
  cleanup, these hooks fail without a stub. Re-created the stubs
  before running pre-commit on this step's files. Step 05 must
  migrate `set_env_context.sh` to source the new pair (`context.env`
  + `context.<side>.env`) so the stubs are no longer needed.

- **No operator startup smoke test from this step.** The plan's step 5
  (`bash scripts/dev/op_run.sh --wait` + tmux capture) requires the
  devc with the new `op_run.sh` env-loading pattern, which is
  step-05 work. The Go-side function compiles and is unit-load-correct
  here; end-to-end "operator logs three Loaded environment variables…
  lines" verification belongs to step 06.

- **`godotenv.Overload` availability.** `joho/godotenv` is at v1.5.1 in
  `go.mod` (`grep godotenv go.mod`), well above the v1.4 floor where
  `Overload` is available. No go.mod changes needed.
