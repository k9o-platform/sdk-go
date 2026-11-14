# k9o.dev/sdk-go/barbican

Official Go SDK for [Kilimanjaro](#31-about-kilimanjaro) Barbican.

Barbican for Go: the free, embeddable L2 guardrail (input normalization, deny-list matching over a signed ruleset,
local verdict, streaming output redactor). Advisory / defense-in-depth, never the sandbox boundary.

All logic and all crypto live in the audited Rust engine (`k9o-keep-core`, uniffi crate `ffi-uniffi`). This package
is a thin wrapper over the `uniffi-bindgen-go` cgo binding. It holds no key and takes no verifier argument:
`LoadRulesetSigned` hands the blob to the engine, which verifies it under a key from a root-signed key manifest.
Refusals are the engine's closed reason tokens in `Error.Reason`, never invented text.

```go
b, err := barbican.New() // no network; *Error{Code: CodeEngineMissing} if the engine is not available
if err != nil { /* barbican.IsEngineMissing(err) */ }
defer b.Close()
b.LoadKeyManifest(jws, uint64(time.Now().Unix()))
_, err = b.LoadRulesetSigned(barbican.SignedRuleset{...}) // *Error{Code: CodeRulesetInvalid, Reason: "unknown_kid", Stage: "ruleset"}
v := b.Inspect(text, nil)
```

## Contents

**A. Get started**

1. [Choose the right package](#1-choose-the-right-package)

**B. Build and test**

2. [Nested module + native engine](#2-nested-module--native-engine)

**C. Reference**

3. [Appendix](#3-appendix)
   - 3.1 [About Kilimanjaro](#31-about-kilimanjaro)

## A. Get started

### 1. Choose the right package

| Package | Use it when you are… | Registry | Source |
| --- | --- | --- | --- |
| `k9o.dev/sdk-go` | Building a backend service: the platform client and Gate. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go) | [sdk-go](https://github.com/k9o-platform/sdk-go) |
| `k9o.dev/sdk-go/kibo` | Encrypting and decrypting with a key you hold in your own KMS. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go/kibo) | [sdk-go/kibo](https://github.com/k9o-platform/sdk-go/tree/main/kibo) |
| `k9o.dev/sdk-go/barbican` | Checking model input and redacting model output on your own machine, with no account. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go/barbican) | [sdk-go/barbican](https://github.com/k9o-platform/sdk-go/tree/main/barbican) |

## B. Build and test

### 2. Nested module + native engine

- `k9o_keep_core_ffi_uniffi/*.go|.h` is the COMMITTED `uniffi-bindgen-go` output (pinned `0.4.0+v0.28.3`, uniffi
  0.28). CI regenerates from the release cdylib and fails on any diff.
- The native library is CI-injected (`gen-cgo-shims.sh` writes per-platform static-link shims) and NEVER committed.
  A build made with `--features test-trust-root` embeds the published TEST root public key; only the shared KAT
  test may link it. `gen-cgo-shims.sh <mod> <artifacts> <trust-kat.json>` refuses any archive that embeds it.
- Built with `CGO_ENABLED=0` the package compiles and `New` returns `CodeEngineMissing`. A cgo build whose library
  is not linked fails at link/load time, before Go runs.

Local test (engine built from keep-core `ffi-uniffi`):

```sh
# test-root engine: full shared-KAT replay
CGO_LDFLAGS="-L$LIB_TEST -lk9o_keep_core_ffi_uniffi -Wl,-rpath,$LIB_TEST" go test -count=1 ./...
# release engine: every signed ruleset / key manifest must be refused
K9O_EXPECT_PRODUCTION=1 CGO_LDFLAGS="-L$LIB_REL -lk9o_keep_core_ffi_uniffi -Wl,-rpath,$LIB_REL" go test -count=1 ./...
CGO_ENABLED=0 go test -count=1 ./...   # typed engine-missing degrade
```
(On macOS set the dylib install name to `@rpath/...` first: `install_name_tool -id`.)

## C. Reference

### 3. Appendix

- [About Kilimanjaro](#31-about-kilimanjaro)

#### 3.1 About Kilimanjaro


Kilimanjaro is a modular developer platform for building secure enterprise applications.
You define AI agents, functions and data pipelines on the platform.
The SDKs let you deploy and invoke them, read and write your data, publish to and subscribe to streams,
sign users in, and encrypt with keys you hold.

It provides four building blocks:

- **Keep**: AI security and governance. Guardrails and policy for the agents you run.
- **Pulse**: data intelligence and real-time stream processing. Streams, analytics and live feeds.
- **Gate**: customer identity and access management. Sign-in and sessions, and automatic data scoping so a signed-in user reaches only their own data.
- **Kibo**: cryptographic keys and secrets management. Client-side encryption with keys you hold, and a secrets vault.
