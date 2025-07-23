## Release from master

```mermaid
%%{
  init: {
    'logLevel': 'debug',
    'theme': 'dark',
    'gitGraph': {
      'showBranches': true,
      'mainBranchName': 'master',
      'parallelCommits': 'true'
    }
  }
}%%
gitGraph
    checkout master
    commit id: "A1" tag:"v1.0.0"
    commit id: "A2"
    commit id: "A3" tag:"v1.1.0"
    commit id: "A4" tag:"v1.2.0"
    commit id: "A5"
    commit id: "A6"
    commit id: "A7" tag:"v2.0.0"
    commit id: "A8"
    commit id: "A9" tag:"v2.1.0"
    commit id: "A10"
    commit id: "A11" tag: "v3.0.0"
```

## Patching previous versions

```mermaid
%%{
  init: {
    'logLevel': 'debug',
    'theme': 'dark',
    'gitGraph': {
      'showBranches': true,
      'mainBranchName': 'master',
      'parallelCommits': 'true'
    }
  }
}%%
gitGraph
    checkout master
    commit id: "A1" tag: "v1.0.0"
    commit id: "A2"
    commit id: "A3" tag: "v1.1.0"
    commit id: "A4" tag: "v1.2.0"
    branch release-1.x
    commit id: "B1" tag: "v1.2.1"
    commit id: "B2"
    commit id: "B3" tag: "v1.2.2"
    checkout master
    commit id: "A5"
    commit id: "A6"
    commit id: "A7" tag:"v2.0.0"
    commit id: "A8"
    commit id: "A9" tag:"v2.1.0"
    branch release-2.x
    commit id: "C1" tag: "v2.1.1"
    commit id: "C2" tag: "v2.1.2"
    checkout master
    commit id: "A10"
    commit id: "A11" tag: "v3.0.0"
```
