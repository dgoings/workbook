# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 0.00 ms, cold p95 0.00 ms, burst 0.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |

## Storage and peak resources

Descriptive measurements with no target. Platform darwin/arm64. Repository state: refs packed with git pack-refs --all, objects packed with git gc --quiet --prune=now.

Object size semantics: rawBytes is the uncompressed Git object content size (%(objectsize)); diskBytes is the size of the object's stored representation including delta and header (%(objectsize:disk)) and excludes per-pack index and header overhead.

Raw `ru_maxrss` unit on this platform: bytes. Block I/O counters (`ru_inblock`, `ru_oublock`) maintained: false.

### 500 tasks by 20 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 20 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 40000 of 40000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 10000 | 4035146 | 2169509 |
| state-blob | 10000 | 5139504 | 2642553 |
| other-blob | 0 | 0 | 0 |
| tree | 10000 | 1040000 | 1091789 |
| commit | 10000 | 3264000 | 2036321 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 40000 | 13478650 | 7940172 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 40000) | 7940216 |
| Pack indexes | 1601096 |
| Pack auxiliary files | 160076 |
| Loose objects (0) | 0 |
| Object directory total | 10542590 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 376832 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 2625536 |
| Validation cache sidecars | 0 |
| **Total disposable** | 3002368 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 158.12 | 29507584 | 29507584 bytes | 0 | 0 | 9266 | 35 | 376832 |
| full-validation | 2137.14 | 94158848 | 94158848 bytes | 0 | 0 | 21363 | 50 | 2625536 |

### 500 tasks by 100 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 100 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 200000 of 200000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 50000 | 20467161 | 10579001 |
| state-blob | 50000 | 26348639 | 13967900 |
| other-blob | 0 | 0 | 0 |
| tree | 50000 | 5200000 | 5459017 |
| commit | 50000 | 16464500 | 10275506 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 200000 | 68480300 | 40281424 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 200000) | 40281468 |
| Pack indexes | 8001096 |
| Pack auxiliary files | 800076 |
| Loose objects (0) | 0 |
| Object directory total | 53283842 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 376832 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 11333632 |
| Validation cache sidecars | 0 |
| **Total disposable** | 11710464 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 150.77 | 32620544 | 32620544 bytes | 0 | 0 | 10126 | 35 | 376832 |
| full-validation | 13670.76 | 520880128 | 520880128 bytes | 0 | 0 | 63026 | 50 | 11333632 |
