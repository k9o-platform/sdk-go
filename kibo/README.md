# k9o.dev/sdk-go/kibo

Official Go SDK for [Kilimanjaro](#86-about-kilimanjaro) Kibo.

Encrypt and decrypt on the client with keys held in your own KMS, so the platform never sees plaintext.

## Contents

**A. Get started**

1. [Links](#1-links)
2. [Install](#2-install)
3. [Choose the right package](#3-choose-the-right-package)
4. [Quick start](#4-quick-start)

**B. Use Kibo**

5. [Encrypt through `k9o.Crypto` on a BYOK or HYOK project](#5-encrypt-through-k9ocrypto-on-a-byok-or-hyok-project)

**C. Build and troubleshoot**

6. [Handling errors](#6-handling-errors)
7. [Building with cgo](#7-building-with-cgo)

**D. Reference**

8. [Appendix](#8-appendix)
   - 8.1 [Error fields](#81-error-fields)
   - 8.2 [Functions](#82-functions)
   - 8.3 [Encrypt input fields](#83-encrypt-input-fields)
   - 8.4 [Decrypt input fields](#84-decrypt-input-fields)
   - 8.5 [Related packages](#85-related-packages)
   - 8.6 [About Kilimanjaro](#86-about-kilimanjaro)
9. [License](#9-license)

## A. Get started

### 1. Links

- Website: <https://kilimanjaro.dev>
- Documentation / Developer Hub: <https://docs.kilimanjaro.dev>
- API: <https://api.k9o.io>
- Console: <https://console.k9o.io>
- Open source
  - Platform: <https://github.com/k9o-platform>
  - Community: <https://github.com/k9o-community>

### 2. Install

```bash
go get k9o.dev/sdk-go/kibo
```

- Requires Go 1.22 or newer and cgo.
- This is a separate module from `k9o.dev/sdk-go`, so the core SDK stays pure Go.
- See [building with cgo](#7-building-with-cgo) for the native library.

### 3. Choose the right package

| Package | Use it when you are… | Registry | Source |
| --- | --- | --- | --- |
| `k9o.dev/sdk-go` | Building a backend service: the platform client and Gate. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go) | [sdk-go](https://github.com/k9o-platform/sdk-go) |
| `k9o.dev/sdk-go/kibo` | Encrypting and decrypting with a key you hold in your own KMS. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go/kibo) | [sdk-go/kibo](https://github.com/k9o-platform/sdk-go/tree/main/kibo) |
| `k9o.dev/sdk-go/barbican` | Checking model input and redacting model output on your own machine, with no account. | [pkg.go.dev](https://pkg.go.dev/k9o.dev/sdk-go/barbican) | [sdk-go/barbican](https://github.com/k9o-platform/sdk-go/tree/main/barbican) |

### 4. Quick start

Encrypt a value with a key in AWS KMS, then decrypt it again.
The cryptography runs in a native library; this package is a thin Go wrapper.

```go
package main

import (
	"fmt"
	"os"
	"time"

	"k9o.dev/sdk-go/kibo"
	ffi "k9o.dev/sdk-go/kibo/kibo_kms_rest_ffi"
)

func main() {
	region := "eu-central-1"
	expires := uint64(time.Now().Add(time.Hour).Unix())

	input := kibo.EncryptInput{
		Provider:        ffi.KmsProviderAwsKms,
		Posture:         ffi.FfiPostureByok,
		KmsCred:         kibo.Credential{Secret: os.Getenv("KMS_TOKEN"), ExpiresAtUnixSecs: expires},
		KeyRef:          kibo.KeyRef{Version: 1, ExternalRef: "arn:aws:kms:eu-central-1:111122223333:key/example"},
		ProviderConfig:  kibo.ProviderConfig{AwsRegion: &region},
		AttestationTier: ffi.FfiAttestationTierSoftware,
		Plaintext:       []byte("hello"),
	}
	encrypted, kerr := kibo.Encrypt(input)
	if kerr != nil {
		fmt.Println(kerr)
		return
	}

	decryptInput := kibo.DecryptInput{
		KmsCred:         input.KmsCred,
		ProviderConfig:  input.ProviderConfig,
		Envelope:        encrypted.Envelope,
		Strategy:        ffi.FfiStrategyLocalEnvelope,
		AttestationTier: ffi.FfiAttestationTierSoftware,
	}
	decrypted, kerr := kibo.Decrypt(decryptInput)
	if kerr != nil {
		fmt.Println(kerr)
		return
	}
	fmt.Println(string(decrypted.Plaintext))
}
```

```text
hello
```

- `Encrypt` returns an opaque envelope in `Envelope`. Store it wherever you like.
- When `Strategy` is left unset on encrypt, `LocalEnvelope` is used. Pass the same strategy to `Decrypt`.
- Decrypt needs the same strategy, attestation tier and encryption context as encrypt.

## B. Use Kibo

### 5. Encrypt through `k9o.Crypto` on a BYOK or HYOK project

The platform never sees the plaintext or the key. The key, your cloud and the exchange are all set here, from your own configuration. The core is a small adapter over the generated `kibo_kms_rest_ffi` package (`AppCryptoSealBlocking`, `AppCryptoOpenBlocking`, `AcquireKmsCredentialBlocking`, `CoreCapabilities`) that satisfies `kibo.AppCore`:

```go
core, kerr := kibo.NewClientCore(kibo.ClientCoreConfig{
	Core:            ffiAdapter{}, // your adapter over kibo_kms_rest_ffi
	Provider:        k9oclient.KeyRefAWS,
	Posture:         "byok",
	KeyRef:          clientcore.KeyRef{Version: 1, ExternalRef: keyArn},
	ProviderConfig:  clientcore.ProviderConfig{AwsRegion: &region},
	Federation:      clientcore.FederationSpec{Provider: "aws", RoleArn: roleArn, SessionName: "app", Region: region, KeyArn: keyArn, DurationSecs: 900},
	ExpectedKeyRefs: []string{keyArn}, // required
	FederatedToken:  clientcore.ViaCrypto(k9o.Crypto),
})
if kerr != nil {
	log.Fatal(kerr)
}
k9o.Crypto.UseClientCore(core)
```

- `ExpectedKeyRefs` is required. Without it the core would open whatever key a stored container names.
- A federated-token response that names another provider or project is refused, never followed.
- The exchange is abandoned after `ExchangeDeadline` (30 s by default) with `KIBO_EXCHANGE_TIMEOUT`.
- A call-level problem returns an error; a failing item carries its own `Code` and `Message`, and `UPSTREAM_KMS_ERROR` is your own KMS.

## C. Build and troubleshoot

### 6. Handling errors

Both functions return a `*kibo.Error`, which is `nil` on success. It implements `error`.
Printing it gives `[code] message`:

```go
_, kerr := kibo.Decrypt(decryptInput)
if kerr != nil {
	switch kerr.Code {
	case "UPSTREAM_KMS_ERROR":
		fmt.Println("your KMS failed:", kerr.Message)
	case "K9O_CRYPTO_POSTURE_FLOOR_VIOLATION":
		fmt.Println("posture too low:", kerr.Context["dimension"])
	default:
		fmt.Println(kerr)
	}
}
```

- `UPSTREAM_KMS_ERROR` means your own KMS call failed.
- `K9O_CRYPTO_POSTURE_FLOOR_VIOLATION` carries `dimension`, `requested` and `declared` in `Context`.
- Other codes come from the platform and are passed through. See [error fields](#81-error-fields).

### 7. Building with cgo

The module links a native library, `libkibo_kms_rest_ffi`, from the `kibo_kms_rest_ffi/` directory.
Point cgo at that directory:

```sh
CGO_CFLAGS="-I$PWD/kibo_kms_rest_ffi" \
CGO_LDFLAGS="-L$PWD/kibo_kms_rest_ffi -lkibo_kms_rest_ffi" \
  go build ./...
```

## D. Reference

### 8. Appendix

- [Error fields](#81-error-fields)
- [Functions](#82-functions)
- [Encrypt input fields](#83-encrypt-input-fields)
- [Decrypt input fields](#84-decrypt-input-fields)
- [Related packages](#85-related-packages)
- [About Kilimanjaro](#86-about-kilimanjaro)

#### 8.1 Error fields

| Field | Type | Meaning |
|---|---|---|
| `Code` | `string` | Error code, for example `UPSTREAM_KMS_ERROR` |
| `Message` | `string` | Human-readable explanation |
| `Context` | `map[string]any` | Extra details, such as the posture dimension |

#### 8.2 Functions

| Function | What it does |
|---|---|
| `kibo.Encrypt(input)` | Encrypt `Plaintext` using your KMS and return an `EncryptOutput` with the envelope |
| `kibo.Decrypt(input)` | Decrypt an envelope and return a `DecryptOutput` with the plaintext |

Provider, posture, strategy and attestation constants live in `k9o.dev/sdk-go/kibo/kibo_kms_rest_ffi`.

#### 8.3 Encrypt input fields

| Field | Meaning |
|---|---|
| `Provider` | KMS provider, for example `ffi.KmsProviderAwsKms` |
| `Posture` | `ffi.FfiPostureByok` or `ffi.FfiPostureHyok` |
| `Strategy` | Optional. `ffi.FfiStrategyLocalEnvelope` is used when unset |
| `OracleMode` | Optional oracle routing mode |
| `KmsCred` | Your short-lived KMS credential (`Secret`, `ExpiresAtUnixSecs`) |
| `KeyRef` | Key version and provider key identifier (`Version`, `ExternalRef`) |
| `ProviderConfig` | Provider settings, such as `AwsRegion` or `VaultAddress` |
| `AttestationTier` | `ffi.FfiAttestationTierSoftware`, `...HardwareBacked` or `...Confidential` |
| `EncryptionContext` | Optional key/value pairs bound to the ciphertext |
| `Plaintext` | The bytes to encrypt |
| `PosturePolicy` | Optional policy checked before encrypting |

#### 8.4 Decrypt input fields

| Field | Meaning |
|---|---|
| `KmsCred` | Your KMS credential |
| `ProviderConfig` | Provider settings |
| `Envelope` | The bytes returned by `Encrypt` |
| `Strategy` | Must match the strategy used to encrypt |
| `AttestationTier` | Must match the tier used to encrypt |
| `EncryptionContext` | Must match the context used to encrypt |

#### 8.5 Related packages

| Module | What it does |
|---|---|
| `k9o.dev/sdk-go` | The core platform SDK |
| `k9o.dev/sdk-go/barbican` | Guardrail that inspects text against a signed ruleset |

#### 8.6 About Kilimanjaro

Kilimanjaro is a modular developer platform for building secure enterprise applications.
You define AI agents, functions and data pipelines on the platform.
The SDKs let you deploy and invoke them, read and write your data, publish to and subscribe to streams, sign users in,
and encrypt with keys you hold.

It provides four building blocks:

- **Keep**: AI security and governance. Guardrails and policy for the agents you run.
- **Pulse**: data intelligence and real-time stream processing. Streams, analytics and live feeds.
- **Gate**: customer identity and access management. Sign-in and sessions, and automatic data scoping
  so a signed-in user reaches only their own data.
- **Kibo**: cryptographic keys and secrets management. Client-side encryption with keys you hold, and a secrets vault.

### 9. License

Apache-2.0 © Formelo Inc. See `LICENSE` and `NOTICE`.
