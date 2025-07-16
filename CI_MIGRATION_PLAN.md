# Evergreen CI Configuration Migration Plan

## Overview
This document outlines a step-by-step migration plan to refactor the current monolithic Evergreen CI configuration into a modular, maintainable structure.

## Current State Analysis

### Existing Files
- `.evergreen.yml` (~2000 lines) - Main configuration
- `.evergreen-functions.yml` (~800 lines) - Function definitions
- `.evergreen-tasks.yml` (~1300 lines) - Task definitions
- `.evergreen-mco.yml` (~150 lines) - MCO-specific tasks
- `.evergreen-periodic-builds.yaml` (~180 lines) - Periodic builds

### Key Pain Points
1. **Monolithic structure** - Hard to navigate and maintain
2. **Repetitive dependency definitions** - Multiple YAML anchors for similar dependencies
3. **Mixed concerns** - Unit tests, E2E tests, builds, and releases intermingled
4. **Complex task groups** - Overlapping setup/teardown logic
5. **Hardcoded values** - Registry URLs and versions scattered throughout

## Target Architecture

```
.evergreen/
├── main.yml                    # Main entry point (minimal)
├── variables/
│   ├── common.yml             # Global variables and anchors
│   ├── dependencies.yml       # Build dependency definitions
│   └── versions.yml           # Version specifications
├── functions/
│   ├── setup.yml              # Setup and environment functions
│   ├── build.yml              # Build-related functions
│   ├── test.yml               # Testing functions
│   └── deploy.yml             # Deployment and release functions
├── tasks/
│   ├── unit-tests.yml         # Unit test task definitions
│   ├── e2e-tests.yml          # E2E test task definitions
│   ├── image-builds.yml       # Image building tasks
│   └── preflight.yml          # Preflight and validation tasks
├── variants/
│   ├── unit-tests.yml         # Unit test build variants
│   ├── e2e-community.yml     # Community E2E variants
│   ├── e2e-enterprise.yml    # Enterprise E2E variants
│   ├── image-builds.yml      # Image building variants
│   └── releases.yml          # Release variants
└── periodic/
    └── scheduled-builds.yml   # Periodic/scheduled builds
```

## Migration Strategy

### Phase 1: Preparation and Validation (Week 1)
**Goal**: Set up infrastructure and validate current configuration

#### Step 1.1: Create Migration Branch
```bash
git checkout -b ci-config-refactor
```

#### Step 1.2: Backup Current Configuration
```bash
mkdir -p .evergreen-backup
cp .evergreen*.yml .evergreen-backup/
cp .evergreen*.yaml .evergreen-backup/
```

#### Step 1.3: Validate Current Configuration
- Run existing CI pipeline to establish baseline
- Document all current build variants and their purposes
- Identify critical paths and dependencies

#### Step 1.4: Create New Directory Structure
```bash
mkdir -p .evergreen/{variables,functions,tasks,variants,periodic}
```

### Phase 2: Extract Variables and Common Definitions (Week 1-2)
**Goal**: Centralize and organize variable definitions

#### Step 2.1: Extract Global Variables
Create `.evergreen/variables/common.yml`:
- Move `exec_timeout_secs` and global parameters
- Extract common YAML anchors (`&setup_group`, `&teardown_group`, etc.)
- Standardize naming conventions

#### Step 2.2: Extract Version Definitions
Create `.evergreen/variables/versions.yml`:
- Move Ops Manager version anchors (`&ops_manager_60_latest`, etc.)
- Centralize all version-related variables
- Add documentation for version update process

#### Step 2.3: Extract Dependency Definitions
Create `.evergreen/variables/dependencies.yml`:
- Move dependency anchors (`&base_om6_dependency`, `&community_dependency`, etc.)
- Simplify and deduplicate dependency patterns
- Create reusable dependency templates

### Phase 3: Modularize Functions (Week 2)
**Goal**: Organize functions by purpose

#### Step 3.1: Extract Setup Functions
Create `.evergreen/functions/setup.yml`:
- `clone`, `download_kube_tools`, `setup_building_host`
- `setup_docker_sbom`, `configure_docker_auth`
- `setup_kubernetes_environment`, `setup_gcloud_cli`

#### Step 3.2: Extract Build Functions
Create `.evergreen/functions/build.yml`:
- `pipeline`, `build_multi_cluster_binary`
- `release_docker_image_to_registry`
- Image building related functions

#### Step 3.3: Extract Test Functions
Create `.evergreen/functions/test.yml`:
- `e2e_test`, `test_golang_unit`
- `preflight_image`, `setup_preflight`
- Test environment setup functions

#### Step 3.4: Extract Deploy Functions
Create `.evergreen/functions/deploy.yml`:
- Release and deployment related functions
- Registry and artifact management functions

### Phase 4: Organize Tasks by Type (Week 2-3)
**Goal**: Group tasks by functionality

#### Step 4.1: Extract Unit Test Tasks
Create `.evergreen/tasks/unit-tests.yml`:
- `unit_tests_golang`, `lint_yaml`, `security_scan`
- Unit test task groups

#### Step 4.2: Extract E2E Test Tasks
Create `.evergreen/tasks/e2e-tests.yml`:
- All E2E test task definitions from `.evergreen-tasks.yml`
- Organize by test type (community, enterprise, multi-cluster)

#### Step 4.3: Extract Image Build Tasks
Create `.evergreen/tasks/image-builds.yml`:
- Image building tasks
- Preflight tasks from `.evergreen-tasks.yml`

#### Step 4.4: Extract MCO Tasks
Create `.evergreen/tasks/mco-tests.yml`:
- Move content from `.evergreen-mco.yml`
- Integrate with main task structure

### Phase 5: Organize Build Variants (Week 3)
**Goal**: Group build variants by purpose

#### Step 5.1: Extract Unit Test Variants
Create `.evergreen/variants/unit-tests.yml`:
- `unit_tests` variant
- Lint and security scan variants

#### Step 5.2: Extract E2E Variants
Create `.evergreen/variants/e2e-community.yml` and `.evergreen/variants/e2e-enterprise.yml`:
- Community E2E variants (KIND-based tests)
- Enterprise E2E variants (Ops Manager tests)
- Multi-cluster variants

#### Step 5.3: Extract Build Variants
Create `.evergreen/variants/image-builds.yml`:
- Image building variants
- Preflight variants

#### Step 5.4: Extract Release Variants
Create `.evergreen/variants/releases.yml`:
- Release-related variants
- OpenShift bundle variants

### Phase 6: Handle Periodic Builds (Week 3)
**Goal**: Integrate periodic builds into new structure

#### Step 6.1: Refactor Periodic Configuration
Create `.evergreen/periodic/scheduled-builds.yml`:
- Move content from `.evergreen-periodic-builds.yaml`
- Align with new function and task structure

### Phase 7: Create Main Configuration (Week 3-4)
**Goal**: Create minimal main entry point

#### Step 7.1: Create New Main Configuration
Create `.evergreen/main.yml`:
```yaml
# Main Evergreen Configuration
exec_timeout_secs: 7200

include:
  # Variables
  - filename: .evergreen/variables/common.yml
  - filename: .evergreen/variables/versions.yml
  - filename: .evergreen/variables/dependencies.yml
  
  # Functions
  - filename: .evergreen/functions/setup.yml
  - filename: .evergreen/functions/build.yml
  - filename: .evergreen/functions/test.yml
  - filename: .evergreen/functions/deploy.yml
  
  # Tasks
  - filename: .evergreen/tasks/unit-tests.yml
  - filename: .evergreen/tasks/e2e-tests.yml
  - filename: .evergreen/tasks/image-builds.yml
  - filename: .evergreen/tasks/mco-tests.yml
  
  # Build Variants
  - filename: .evergreen/variants/unit-tests.yml
  - filename: .evergreen/variants/e2e-community.yml
  - filename: .evergreen/variants/e2e-enterprise.yml
  - filename: .evergreen/variants/image-builds.yml
  - filename: .evergreen/variants/releases.yml

parameters:
  - key: evergreen_retry
    value: "true"
    description: set this to false to suppress retries on failure
```

### Phase 8: Testing and Validation (Week 4)
**Goal**: Ensure new configuration works correctly

#### Step 8.1: Syntax Validation
```bash
# Validate YAML syntax
yamllint .evergreen/

# Test Evergreen configuration parsing
evergreen validate --file .evergreen/main.yml
```

#### Step 8.2: Incremental Testing
- Test individual components (unit tests first)
- Validate task dependencies
- Test build variants one by one

#### Step 8.3: Full Pipeline Testing
- Run complete CI pipeline with new configuration
- Compare results with baseline from Phase 1
- Fix any issues discovered

### Phase 9: Migration and Cleanup (Week 4)
**Goal**: Switch to new configuration and clean up

#### Step 9.1: Update Main Evergreen File
Replace `.evergreen.yml` content:
```yaml
include:
  - filename: .evergreen/main.yml
```

#### Step 9.2: Archive Old Configuration
```bash
mkdir -p .evergreen-legacy
mv .evergreen-functions.yml .evergreen-legacy/
mv .evergreen-tasks.yml .evergreen-legacy/
mv .evergreen-mco.yml .evergreen-legacy/
mv .evergreen-periodic-builds.yaml .evergreen-legacy/
```

#### Step 9.3: Update Documentation
- Update README with new CI structure
- Document maintenance procedures
- Create troubleshooting guide

## Risk Mitigation

### Rollback Plan
1. Keep original files in `.evergreen-backup/`
2. Maintain feature branch until validation complete
3. Quick rollback procedure documented

### Testing Strategy
1. **Incremental validation** - Test each phase separately
2. **Parallel testing** - Run old and new configs side by side
3. **Critical path focus** - Prioritize release and main branch workflows

### Communication Plan
1. **Team notification** - Inform team before starting migration
2. **Progress updates** - Weekly status updates
3. **Training session** - Walk through new structure with team

## Success Criteria

### Technical Metrics
- [ ] All existing functionality preserved
- [ ] CI pipeline execution time unchanged or improved
- [ ] Configuration files reduced in size by >50%
- [ ] Zero breaking changes to existing workflows

### Maintainability Metrics
- [ ] New team members can understand structure in <30 minutes
- [ ] Adding new test takes <5 minutes
- [ ] Configuration changes isolated to single files
- [ ] Dependency management centralized and clear

## Timeline Summary

| Phase | Duration | Key Deliverables |
|-------|----------|------------------|
| 1 | Week 1 | Preparation, validation, directory structure |
| 2 | Week 1-2 | Variables extracted and organized |
| 3 | Week 2 | Functions modularized |
| 4 | Week 2-3 | Tasks organized by type |
| 5 | Week 3 | Build variants restructured |
| 6 | Week 3 | Periodic builds integrated |
| 7 | Week 3-4 | Main configuration created |
| 8 | Week 4 | Testing and validation |
| 9 | Week 4 | Migration and cleanup |

**Total Duration**: 4 weeks

## Next Steps

1. **Review and approve** this migration plan
2. **Schedule migration window** - Coordinate with team
3. **Begin Phase 1** - Create migration branch and backup
4. **Set up monitoring** - Track migration progress and issues

---

*This migration plan ensures a systematic, low-risk approach to modernizing the Evergreen CI configuration while maintaining all existing functionality.*
