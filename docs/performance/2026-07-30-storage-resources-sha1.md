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

### 500 tasks by 20 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 20 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 40000 of 40000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 10000 | 4035146 | 2169509 |
| state-blob | 10000 | 5139504 | 2642553 |
| other-blob | 0 | 0 | 0 |
| tree | 10000 | 800000 | 838795 |
| commit | 10000 | 2796000 | 1755064 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 40000 | 12770650 | 7405921 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 40000) | 7405953 |
| Pack indexes | 1121072 |
| Pack auxiliary files | 160052 |
| Loose objects (0) | 0 |
| Object directory total | 9288243 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 360448 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 2097152 |
| Validation cache sidecars | 0 |
| **Total disposable** | 2457600 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 141.15 | 29212672 | 29212672 bytes | 0 | 0 | 9183 | 36 | 360448 |
| full-validation | 2112.66 | 91799552 | 91799552 bytes | 0 | 0 | 20835 | 52 | 2097152 |

### 500 tasks by 100 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 100 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 200000 of 200000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 50000 | 20467161 | 10579001 |
| state-blob | 50000 | 26348639 | 13967900 |
| other-blob | 0 | 0 | 0 |
| tree | 50000 | 4000000 | 4194067 |
| commit | 50000 | 14076500 | 8841617 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 200000 | 64892300 | 37582585 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 200000) | 37582617 |
| Pack indexes | 5601072 |
| Pack auxiliary files | 800052 |
| Loose objects (0) | 0 |
| Object directory total | 46984907 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 360448 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 8654848 |
| Validation cache sidecars | 0 |
| **Total disposable** | 9015296 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 148.85 | 29048832 | 29048832 bytes | 0 | 0 | 9790 | 36 | 360448 |
| full-validation | 12873.77 | 469729280 | 469729280 bytes | 0 | 0 | 58739 | 52 | 8654848 |
