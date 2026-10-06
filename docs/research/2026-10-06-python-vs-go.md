# Python versus Go for the N-WAN controller

Date: 2026-10-06. This is a comparison and advisory recommendation, not an implemented release or a finalized language decision.

## Conditions declared before comparing

Compare the same controller, the same probe budget, the same state history, the same policy adapter and the same authenticated LuCI JS/rpcd UI. The initial target is OpenWrt 24.10.8 on aarch64 Cortex-A53, with about 1 GiB RAM and about 75 MiB free overlay space at the last device inspection.

Priority: reliable deployment and long-running operation first; developer convenience and release complexity second. Do not assign invented resource or correctness scores. Missing executable-size, RSS, CPU and fault-injection measurements remain explicitly unknown.

Three independent reviewers evaluated both languages for deployment/resources, reliability/testing, and maintenance/releases using the same product scope.

## Side-by-side comparison

| Dimension | Python 3 | Go without cgo |
|---|---|---|
| Installation | Interpreter, libpython and selected stdlib modules must be installed | A compiled executable can include its runtime; no Go installation on the router |
| State/invariant checks | Runtime validation, tests and a CI type checker | Compiler type checks plus the same required runtime validation and tests |
| Concurrent probes | asyncio can implement bounded asynchronous work; blocking calls require isolation | Goroutines, contexts, HTTP timeouts and bounded workers fit a long-running daemon |
| HTTP/time budgets | A total probe deadline needs deliberate client/process design | net/http provides a whole-request timeout, but it must be configured; contexts are cooperative |
| Locking and persistence | flock, atomic replace and fsync are available | Linux locking, atomic rename and file sync are available |
| Correct policy application | Requires shared locking, prevalidation, atomic transaction, readback and rollback | The same requirements apply; language does not guarantee routing correctness |
| Development iteration | Source changes can be run without compiling | Recompilation is required; structured state and interfaces help larger changes |
| Release matrix | Pure source payload can be architecture-independent, but runtime packages must exist | Separate artifacts for each supported architecture/ISA; CI can cross-build them |
| LuCI interface | Same JS page, RPC methods and ACL | Same JS page, RPC methods and ACL |
| Application size, RSS and CPU | Not measured | Not measured |

## Verified Python dependency metadata

The official OpenWrt 24.10.8 aarch64_cortex-a53 package manifest lists Python 3.11.16-r1:

| Package | Installed-Size metadata, bytes |
|---|---:|
| libpython3-3.11 | 4,679,680 |
| python3-base | 1,126,400 |
| python3-light | 9,768,960 |
| Sum | 15,575,040, approximately 14.85 MiB |
| Additional python3-asyncio | 706,560 |
| Additional python3-logging | 225,280 |

These are package metadata, not measured occupied flash. They exclude our daemon/UI and any missing libbz2, zlib, libpthread or further imported modules. A smaller custom Python dependency set may be possible but requires an import audit; base alone is not evidence that the complete application runs.

[Exact package manifest](https://downloads.openwrt.org/releases/24.10.8/packages/aarch64_cortex-a53/packages/Packages), [OpenWrt Python definitions](https://raw.githubusercontent.com/openwrt/packages/openwrt-24.10/lang/python/python3/Makefile).

## Go deployment caveats

The intended autonomous profile would be CGO_ENABLED=0 with no native ubus/UCI bindings. For the initial target: GOOS=linux, GOARCH=arm64, conservative ARMv8.0 ISA. Every built artifact must be checked for target architecture and unexpected dynamic-loader/shared-library requirements.

Standard OpenWrt 24.10 golang-package.mk enables cgo and external linking by default. Therefore an SDK package does not automatically deliver the above autonomous profile; its build configuration must explicitly preserve it. Some OpenWrt architectures are not covered by Go's supported-target matrix.

[Go/OpenWrt build helper](https://raw.githubusercontent.com/openwrt/packages/openwrt-24.10/lang/golang/golang-package.mk), [architecture mapping](https://raw.githubusercontent.com/openwrt/packages/openwrt-24.10/lang/golang/golang-values.mk), [Go cgo documentation](https://pkg.go.dev/cmd/cgo), [minimum requirements](https://go.dev/wiki/MinimumRequirements).

## Reliability boundaries

Neither language turns cancellation into an unconditional wall-clock guarantee for arbitrary code. Both must limit concurrency and output, terminate/reap child processes, bound downloaded payloads, reserve persistent budgets, recover after interruption and keep the measured-generation/policy transaction consistent.

Go's standard net/http Client.Timeout covers connection setup, redirects and body reading. Python's asyncio.wait_for can exceed the nominal timeout while waiting for cancellation. Python remains viable with a deliberately bounded async client or supervised curl process; comparing that process backend to Go using the same curl backend is fairer than crediting language alone for all probe behavior.

[Go HTTP client](https://pkg.go.dev/net/http#Client), [Go command lifecycle](https://pkg.go.dev/os/exec#Cmd), [Python cancellation](https://docs.python.org/3.11/library/asyncio-task.html#asyncio.wait_for), [Python async subprocesses](https://docs.python.org/3.11/library/asyncio-subprocess.html).

Binding a source IP alone does not prove WAN selection. DNS, socket device/mark, redirects, connection reuse and actual egress must be verified. In a native pure-Go probe, the socket options must be implemented explicitly; a libc LD_PRELOAD wrapper must not be assumed to intercept Go's direct system calls. Using curl through mwan3 use is an alternative for either language and still requires route validation.

[Go dialer](https://pkg.go.dev/net#Dialer), [Go resolver](https://pkg.go.dev/net#Resolver), [Python sockets](https://docs.python.org/3.11/library/socket.html#socket.socket.setsockopt).

## Recommendation and remaining choice

Both options are feasible on the initial router. Python retains an advantage for rapid experimentation and a single source payload. Go retains an advantage for the declared priorities: a long-running controller with an explicit state model, standard concurrent testing and a deliberately self-contained router artifact.

Recommend Go for the maintained controller, with a separate minimal LuCI frontend. Do not claim lower RAM, smaller total bytes or faster networking until equivalent implementations and their helper processes are measured on the target. The implementation language still requires the user's choice before implementation proceeds.
