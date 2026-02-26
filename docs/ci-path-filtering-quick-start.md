# CI Path Filtering - Quick Start Guide

## Overview

This system allows CI to skip expensive tests (e2e, image builds) when PRs only change documentation or CI configuration files.

## How It Works

1. **Single unified script** (`should_run.sh`) analyzes changed files using git diff
2. **Always runs on merged code**: If on master/base branch, always runs (no filtering)
3. **JSON config file** (`.evergreen-path-filter.json`) defines patterns for each scope
4. **Script takes scope argument** (e2e_tests, unit_tests, build_images) and optional base commit
5. **Evergreen uses script exit codes** to decide whether to run tests
   - Exit code 0 = run tests
   - Exit code != 0 = skip tests

## Quick Test

```bash
# Test with a docs-only change
git checkout -b test-docs
echo "# Test" >> README.md
git add README.md
git commit -m "Test docs"

# Should exit with code 1 (skip e2e)
./scripts/evergreen/should_run.sh e2e_tests
echo $?  # Should print 1

# Should exit with code 1 (skip builds)  
./scripts/evergreen/should_run.sh build_images
echo $?  # Should print 1

# Should exit with code 1 (skip unit tests)
./scripts/evergreen/should_run.sh unit_tests
echo $?  # Should print 1
```

## Integration Options

### Option A: Variant-Level (Recommended for E2E)

Add to variant definition in `.evergreen.yml`:

```yaml
- name: e2e_mdb_community
  activate:
    - func: clone
    - func: setup_jq  # jq is needed for should_run.sh
    - command: shell.exec
      params:
        shell: bash
        working_dir: src/github.com/mongodb/mongodb-kubernetes
        script: scripts/evergreen/should_run.sh e2e_tests
  # ... rest of variant config
```

**When to use**: Expensive variants (e2e tests, image builds)

### Option B: Task-Level Early Exit

Add to task commands:

```yaml
commands:
  - func: setup_jq  # jq is needed for should_run.sh
  - command: shell.exec
    params:
      shell: bash
      working_dir: src/github.com/mongodb/mongodb-kubernetes
      script: |
        if ! scripts/evergreen/should_run.sh e2e_tests; then
          echo "Skipping - only docs changed"
          exit 0
        fi
  # ... rest of commands
```

**When to use**: Cheaper tasks (unit tests, lint)

### Option C: Dynamic Task Addition

Use `run_task_conditionally`:

```yaml
- func: run_task_conditionally
  vars:
    condition_script: scripts/evergreen/should_run.sh e2e_tests
    variant: e2e_mdb_community
    task: e2e_mdb_community_task_group
```

**When to use**: When you need to conditionally add tasks to variants

## Available Scopes

| Scope | Purpose | Skip When |
|-------|---------|-----------|
| `e2e_tests` | E2E test filtering | Docs-only, CI-config-only changes |
| `unit_tests` | Unit test filtering | Docs-only changes |
| `build_images` | Image build filtering | Docs-only, CI-config-only changes |

## Configuration

Patterns are defined in `.evergreen-path-filter.json`. Each scope has:
- `trigger_patterns`: File patterns that require the scope to run
- Logic: If **any** changed file matches a trigger pattern → run, otherwise skip

**Important**: The script always runs tests when:
- On the base branch (e.g., on `master` comparing against `origin/master`)
- HEAD matches the base commit (merged code)

Path-based filtering only applies to PRs with changes.

To modify patterns, edit the JSON file - no script changes needed!

## File Patterns

### Always Skip E2E/Builds
- `docs/**`
- `*.md` (except in code dirs)
- `README*`, `CONTRIBUTING*`, `CHANGELOG*`

### Always Run Tests
- `controllers/**`, `pkg/**` (Go code)
- `*.go`, `go.mod`, `go.sum`
- `test/e2e/**`
- `Dockerfile*`
- `scripts/release/**`

## Rollout Plan

1. ✅ **Phase 1**: Create scripts (Done)
2. ⏳ **Phase 2**: Test scripts locally
3. ⏳ **Phase 3**: Integrate one variant (e.g., `e2e_mdb_community`)
4. ⏳ **Phase 4**: Test on real PRs
5. ⏳ **Phase 5**: Roll out to all e2e variants
6. ⏳ **Phase 6**: Add to image build variants
7. ⏳ **Phase 7**: Monitor and refine patterns

## Troubleshooting

**Problem**: Tests skipped when they shouldn't be
- **Solution**: Add missing paths to `trigger_patterns` in `.evergreen-path-filter.json`
- **Override**: Modify JSON config (no script changes needed)

**Problem**: Tests run when they shouldn't be  
- **Solution**: Remove patterns from `trigger_patterns` that are too broad
- **Override**: Make trigger patterns more specific

**Problem**: Script fails in Evergreen
- **Solution**: Ensure `setup_jq` runs before calling `should_run.sh`
- **Solution**: Ensure `git fetch origin` works, check base commit detection

**Problem**: "jq is required but not found"
- **Solution**: Add `func: setup_jq` before calling `should_run.sh`

## See Also

- `ci-path-filtering-concrete-example.yml` - Before/after YAML examples for copy-paste
