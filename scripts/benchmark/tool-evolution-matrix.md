## Tool Evolution Benchmark Matrix

Use these checkpoints to measure whether the expanded tool surface helped or hurt Terminal-Bench.

### Primary baseline

- `3dd0d4d01fb23c67b194f1f315ce8c4faaa2df75`
  - Last pure 4-tool build before any navigation or benchmark-specific behavior.
  - Tool surface: `read`, `write`, `edit`, `bash`
  - Required preset: `agent`

### Adjacent control

- `401db8659f4c18fbfacee55c53bbc7c2c143b40a`
  - Earlier 4-tool build from the rename cut.
  - Tool surface: `read`, `write`, `edit`, `bash`
  - Required preset: `agent`

### Expansion ladder

- `4eccccd46572c943e25b772a758d5d7e156effed`
  - First navigation-tools build.
  - Tool surface: `read`, `write`, `edit`, `bash`, `find`, `grep`, `ls`
  - Required preset: `agent`

- `dd1144800d5fe2e6745537ff9366f9a325eb76d1`
  - First benchmark-preset build.
  - Tool surface: `read`, `write`, `edit`, `bash`, `find`, `grep`, `ls`
  - Required preset: `benchmark`

- `42f8e484d9034dfbdb9c457618605bc7b4fa3f53`
  - First patch-tool build.
  - Tool surface: `read`, `write`, `edit`, `bash`, `find`, `grep`, `ls`, `patch`
  - Required preset: `benchmark`

- `dcc2f4512207e8b80d9f70bb2aeb7cb6c3913077`
  - First task-tool build.
  - Tool surface: `read`, `write`, `edit`, `bash`, `find`, `grep`, `ls`, `patch`, `task`
  - Required preset: `benchmark`

### Current target

- Current working tree
  - Use the checked-out tree after the local benchmark harness fixes and benchmark-mode `task` exclusion.
  - Required preset: `benchmark`

## Run order

1. `3dd0d4d01fb23c67b194f1f315ce8c4faaa2df75`
2. `4eccccd46572c943e25b772a758d5d7e156effed`
3. `dd1144800d5fe2e6745537ff9366f9a325eb76d1`
4. `42f8e484d9034dfbdb9c457618605bc7b4fa3f53`
5. Current working tree

Use `401db8659f4c18fbfacee55c53bbc7c2c143b40a` only if the two 4-tool runs disagree enough that we need to rule out unrelated churn inside the 4-tool era.
