package kibo

import (
	"errors"
	"fmt"

	"k9o.dev/sdk-go/kibo/clientcore"
	ffi "k9o.dev/sdk-go/kibo/kibo_kms_rest_ffi"
)

// FFIAppCore is the AppCore over the generated kibo_kms_rest_ffi binding. Pass it
// as ClientCoreConfig.Core to run the client path against the real core.
type FFIAppCore struct{}

var _ AppCore = FFIAppCore{}

func ffiProvider(p string) (ffi.KmsProvider, error) {
	switch p {
	case "aws-kms":
		return ffi.KmsProviderAwsKms, nil
	case "gcp-kms":
		return ffi.KmsProviderGcpKms, nil
	case "azure-kv":
		return ffi.KmsProviderAzureKv, nil
	case "hashicorp-vault":
		return ffi.KmsProviderHashicorpVault, nil
	}
	return 0, fmt.Errorf("K9O_INVALID_FIELD_VALUE: unknown provider %q", p)
}

func ffiPosture(p string) (ffi.FfiPosture, error) {
	switch p {
	case "byok":
		return ffi.FfiPostureByok, nil
	case "hyok":
		return ffi.FfiPostureHyok, nil
	}
	return 0, fmt.Errorf("K9O_INVALID_FIELD_VALUE: unknown posture %q", p)
}

func ffiCred(c clientcore.Credential) ffi.FfiCredential {
	return ffi.FfiCredential{Secret: c.Secret, ExpiresAtUnixSecs: c.ExpiresAtUnixSecs}
}

func ffiProviderConfig(c clientcore.ProviderConfig) ffi.FfiProviderConfig {
	return ffi.FfiProviderConfig{AwsRegion: c.AwsRegion, VaultAddress: c.VaultAddress}
}

// coreErr turns the binding's error into "<CODE>: <message>", the text the client
// core reads. A platform code passes through; your own KMS failing is UPSTREAM_KMS_ERROR.
func coreErr(err error) error {
	var pv *ffi.ZkFfiUniffiErrorPostureViolation
	var pl *ffi.ZkFfiUniffiErrorPlatform
	var uk *ffi.ZkFfiUniffiErrorUpstreamKms
	switch {
	case errors.As(err, &pv):
		return fmt.Errorf("%s: %s", pv.Code, pv.Detail)
	case errors.As(err, &pl):
		return fmt.Errorf("%s: %s", pl.Code, pl.Detail)
	case errors.As(err, &uk):
		return fmt.Errorf("UPSTREAM_KMS_ERROR: %s", uk.Detail)
	}
	return err
}

func (FFIAppCore) AppCryptoSealBlocking(in clientcore.AppSealInput) (clientcore.AppSealOutput, error) {
	prov, err := ffiProvider(in.Provider)
	if err != nil {
		return clientcore.AppSealOutput{}, err
	}
	post, err := ffiPosture(in.Posture)
	if err != nil {
		return clientcore.AppSealOutput{}, err
	}
	out, err := ffi.AppCryptoSealBlocking(ffi.AppSealInput{
		Provider:        prov,
		Posture:         post,
		KmsCred:         ffiCred(in.KmsCred),
		KeyRef:          ffi.FfiKeyRef{Version: uint64(in.KeyRef.Version), ExternalRef: in.KeyRef.ExternalRef},
		ProviderConfig:  ffiProviderConfig(in.ProviderConfig),
		ProjectId:       in.ProjectID,
		Crypt:           in.Crypt,
		Context:         in.Context,
		Plaintext:       in.Plaintext,
		ExpectedKeyRefs: in.ExpectedKeyRefs,
	})
	if err != nil {
		return clientcore.AppSealOutput{}, coreErr(err)
	}
	return clientcore.AppSealOutput{Container: out.Container}, nil
}

func (FFIAppCore) AppCryptoOpenBlocking(in clientcore.AppOpenInput) (clientcore.AppOpenOutput, error) {
	out, err := ffi.AppCryptoOpenBlocking(ffi.AppOpenInput{
		KmsCred:         ffiCred(in.KmsCred),
		ProviderConfig:  ffiProviderConfig(in.ProviderConfig),
		ProjectId:       in.ProjectID,
		Crypt:           in.Crypt,
		Context:         in.Context,
		Container:       in.Container,
		ExpectedKeyRefs: in.ExpectedKeyRefs,
	})
	if err != nil {
		return clientcore.AppOpenOutput{}, coreErr(err)
	}
	return clientcore.AppOpenOutput{Plaintext: out.Plaintext}, nil
}

func (FFIAppCore) AcquireKmsCredentialBlocking(in clientcore.FederationInput) (clientcore.Credential, error) {
	var spec ffi.FfiFederationSpec
	s := in.Spec
	switch s.Provider {
	case "aws":
		spec = ffi.FfiFederationSpecAws{RoleArn: s.RoleArn, SessionName: s.SessionName, Region: s.Region, KeyArn: s.KeyArn, DurationSecs: s.DurationSecs}
	case "gcp":
		spec = ffi.FfiFederationSpecGcp{Audience: s.Audience}
	case "azure":
		spec = ffi.FfiFederationSpecAzure{TenantId: s.TenantID, ClientId: s.ClientID, Scope: s.Scope}
	case "vault":
		spec = ffi.FfiFederationSpecVault{VaultAddr: s.VaultAddr, Mount: s.JwtAuthMount, Role: s.Role}
	default:
		return clientcore.Credential{}, fmt.Errorf("K9O_INVALID_FIELD_VALUE: unknown federation provider %q", s.Provider)
	}
	out, err := ffi.AcquireKmsCredentialBlocking(ffi.FfiFederationInput{Spec: spec, IdentityToken: in.IdentityToken, NowUnixSecs: in.NowUnixSecs})
	if err != nil {
		return clientcore.Credential{}, coreErr(err)
	}
	return clientcore.Credential{Secret: out.Secret, ExpiresAtUnixSecs: out.ExpiresAtUnixSecs}, nil
}

func (FFIAppCore) CoreCapabilities() []string { return ffi.CoreCapabilities() }
