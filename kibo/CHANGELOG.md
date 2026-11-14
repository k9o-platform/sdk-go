# Changelog — k9o.dev/sdk-go/kibo

## 1.5.2

### Added

- linux/arm64: the module ships the static archive for `aarch64-unknown-linux-gnu` and its cgo link flags (`lib/linux_arm64/libkibo_kms_rest_ffi.a`). `v1.5.1` links on darwin/arm64, darwin/amd64 and linux/amd64 only.

## 1.5.1

### Fixed

- The module now ships the native static archives (`lib/<os>_<arch>/libkibo_kms_rest_ffi.a`). `v1.5.0` was published without them and does not link; it is retracted. Use `v1.5.1`.

## 1.5.0

### Added

- `kibo.NewClientCore` (package `k9o.dev/sdk-go/kibo/clientcore`) is the client core for `Crypto` on a BYOK or HYOK project: it seals and opens on this machine against your own KMS or HSM. It needs a core built from `zk-bundle-v2.15.0` or later, passed in as an `AppCore`. `ExpectedKeyRefs` is required and non-empty, and the configured key must be in it; the core is refused before any token is fetched if it does not list `expected_key_refs` (`K9O_CRYPTO_KEY_PIN_UNENFORCED`) or is missing. The key, provider, region, role and endpoint come from your configuration only: a token response naming another `Provider` or `ProjectID` is refused with `K9O_INVALID_FIELD_VALUE` and nothing is exchanged. The exchange is abandoned after `ExchangeDeadline` (default 30 s) with `KIBO_EXCHANGE_TIMEOUT`; the credential is reused until 60 s before it expires, and a token is fetched on every call. An item that fails carries the core's code (`UPSTREAM_KMS_ERROR` is your own KMS). Neither the token nor the credential appears in an error or when printed.

### Changed

- README description.
