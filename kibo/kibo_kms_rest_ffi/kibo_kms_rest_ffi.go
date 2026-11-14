package kibo_kms_rest_ffi

// #include <kibo_kms_rest_ffi.h>
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"runtime/cgo"
	"unsafe"
)

// This is needed, because as of go 1.24
// type RustBuffer C.RustBuffer cannot have methods,
// RustBuffer is treated as non-local type
type GoRustBuffer struct {
	inner C.RustBuffer
}

type RustBufferI interface {
	AsReader() *bytes.Reader
	Free()
	ToGoBytes() []byte
	Data() unsafe.Pointer
	Len() uint64
	Capacity() uint64
}

func RustBufferFromExternal(b RustBufferI) GoRustBuffer {
	return GoRustBuffer{
		inner: C.RustBuffer{
			capacity: C.uint64_t(b.Capacity()),
			len:      C.uint64_t(b.Len()),
			data:     (*C.uchar)(b.Data()),
		},
	}
}

func (cb GoRustBuffer) Capacity() uint64 {
	return uint64(cb.inner.capacity)
}

func (cb GoRustBuffer) Len() uint64 {
	return uint64(cb.inner.len)
}

func (cb GoRustBuffer) Data() unsafe.Pointer {
	return unsafe.Pointer(cb.inner.data)
}

func (cb GoRustBuffer) AsReader() *bytes.Reader {
	b := unsafe.Slice((*byte)(cb.inner.data), C.uint64_t(cb.inner.len))
	return bytes.NewReader(b)
}

func (cb GoRustBuffer) Free() {
	rustCall(func(status *C.RustCallStatus) bool {
		C.ffi_kibo_kms_rest_ffi_rustbuffer_free(cb.inner, status)
		return false
	})
}

func (cb GoRustBuffer) ToGoBytes() []byte {
	return C.GoBytes(unsafe.Pointer(cb.inner.data), C.int(cb.inner.len))
}

func stringToRustBuffer(str string) C.RustBuffer {
	return bytesToRustBuffer([]byte(str))
}

func bytesToRustBuffer(b []byte) C.RustBuffer {
	if len(b) == 0 {
		return C.RustBuffer{}
	}
	// We can pass the pointer along here, as it is pinned
	// for the duration of this call
	foreign := C.ForeignBytes{
		len:  C.int(len(b)),
		data: (*C.uchar)(unsafe.Pointer(&b[0])),
	}

	return rustCall(func(status *C.RustCallStatus) C.RustBuffer {
		return C.ffi_kibo_kms_rest_ffi_rustbuffer_from_bytes(foreign, status)
	})
}

type BufLifter[GoType any] interface {
	Lift(value RustBufferI) GoType
}

type BufLowerer[GoType any] interface {
	Lower(value GoType) C.RustBuffer
}

type BufReader[GoType any] interface {
	Read(reader io.Reader) GoType
}

type BufWriter[GoType any] interface {
	Write(writer io.Writer, value GoType)
}

func LowerIntoRustBuffer[GoType any](bufWriter BufWriter[GoType], value GoType) C.RustBuffer {
	// This might be not the most efficient way but it does not require knowing allocation size
	// beforehand
	var buffer bytes.Buffer
	bufWriter.Write(&buffer, value)

	bytes, err := io.ReadAll(&buffer)
	if err != nil {
		panic(fmt.Errorf("reading written data: %w", err))
	}
	return bytesToRustBuffer(bytes)
}

func LiftFromRustBuffer[GoType any](bufReader BufReader[GoType], rbuf RustBufferI) GoType {
	defer rbuf.Free()
	reader := rbuf.AsReader()
	item := bufReader.Read(reader)
	if reader.Len() > 0 {
		// TODO: Remove this
		leftover, _ := io.ReadAll(reader)
		panic(fmt.Errorf("Junk remaining in buffer after lifting: %s", string(leftover)))
	}
	return item
}

func rustCallWithError[E any, U any](converter BufReader[*E], callback func(*C.RustCallStatus) U) (U, *E) {
	var status C.RustCallStatus
	returnValue := callback(&status)
	err := checkCallStatus(converter, status)
	return returnValue, err
}

func checkCallStatus[E any](converter BufReader[*E], status C.RustCallStatus) *E {
	switch status.code {
	case 0:
		return nil
	case 1:
		return LiftFromRustBuffer(converter, GoRustBuffer{inner: status.errorBuf})
	case 2:
		// when the rust code sees a panic, it tries to construct a rustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{inner: status.errorBuf})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		panic(fmt.Errorf("unknown status code: %d", status.code))
	}
}

func checkCallStatusUnknown(status C.RustCallStatus) error {
	switch status.code {
	case 0:
		return nil
	case 1:
		panic(fmt.Errorf("function not returning an error returned an error"))
	case 2:
		// when the rust code sees a panic, it tries to construct a C.RustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{
				inner: status.errorBuf,
			})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		return fmt.Errorf("unknown status code: %d", status.code)
	}
}

func rustCall[U any](callback func(*C.RustCallStatus) U) U {
	returnValue, err := rustCallWithError[error](nil, callback)
	if err != nil {
		panic(err)
	}
	return returnValue
}

type NativeError interface {
	AsError() error
}

func writeInt8(writer io.Writer, value int8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint8(writer io.Writer, value uint8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt16(writer io.Writer, value int16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint16(writer io.Writer, value uint16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt32(writer io.Writer, value int32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint32(writer io.Writer, value uint32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt64(writer io.Writer, value int64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint64(writer io.Writer, value uint64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat32(writer io.Writer, value float32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat64(writer io.Writer, value float64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func readInt8(reader io.Reader) int8 {
	var result int8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint8(reader io.Reader) uint8 {
	var result uint8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt16(reader io.Reader) int16 {
	var result int16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint16(reader io.Reader) uint16 {
	var result uint16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt32(reader io.Reader) int32 {
	var result int32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint32(reader io.Reader) uint32 {
	var result uint32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt64(reader io.Reader) int64 {
	var result int64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint64(reader io.Reader) uint64 {
	var result uint64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat32(reader io.Reader) float32 {
	var result float32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat64(reader io.Reader) float64 {
	var result float64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func init() {

	uniffiCheckChecksums()
}

func uniffiCheckChecksums() {
	// Get the bindings contract version from our ComponentInterface
	bindingsContractVersion := 26
	// Get the scaffolding contract version by calling the into the dylib
	scaffoldingContractVersion := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint32_t {
		return C.ffi_kibo_kms_rest_ffi_uniffi_contract_version()
	})
	if bindingsContractVersion != int(scaffoldingContractVersion) {
		// If this happens try cleaning and rebuilding your project
		panic("kibo_kms_rest_ffi: UniFFI contract version mismatch")
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_acquire_kms_credential()
		})
		if checksum != 56968 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_acquire_kms_credential: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_acquire_kms_credential_blocking()
		})
		if checksum != 64418 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_acquire_kms_credential_blocking: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_open()
		})
		if checksum != 281 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_open: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_open_blocking()
		})
		if checksum != 9795 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_open_blocking: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_seal()
		})
		if checksum != 57077 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_seal: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_seal_blocking()
		})
		if checksum != 55736 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_app_crypto_seal_blocking: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_core_capabilities()
		})
		if checksum != 57567 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_core_capabilities: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_zk_decrypt()
		})
		if checksum != 8128 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_zk_decrypt: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_zk_decrypt_blocking()
		})
		if checksum != 63808 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_zk_decrypt_blocking: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_zk_encrypt()
		})
		if checksum != 65185 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_zk_encrypt: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_kibo_kms_rest_ffi_checksum_func_zk_encrypt_blocking()
		})
		if checksum != 54723 {
			// If this happens try cleaning and rebuilding your project
			panic("kibo_kms_rest_ffi: uniffi_kibo_kms_rest_ffi_checksum_func_zk_encrypt_blocking: UniFFI API checksum mismatch")
		}
	}
}

type FfiConverterUint32 struct{}

var FfiConverterUint32INSTANCE = FfiConverterUint32{}

func (FfiConverterUint32) Lower(value uint32) C.uint32_t {
	return C.uint32_t(value)
}

func (FfiConverterUint32) Write(writer io.Writer, value uint32) {
	writeUint32(writer, value)
}

func (FfiConverterUint32) Lift(value C.uint32_t) uint32 {
	return uint32(value)
}

func (FfiConverterUint32) Read(reader io.Reader) uint32 {
	return readUint32(reader)
}

type FfiDestroyerUint32 struct{}

func (FfiDestroyerUint32) Destroy(_ uint32) {}

type FfiConverterUint64 struct{}

var FfiConverterUint64INSTANCE = FfiConverterUint64{}

func (FfiConverterUint64) Lower(value uint64) C.uint64_t {
	return C.uint64_t(value)
}

func (FfiConverterUint64) Write(writer io.Writer, value uint64) {
	writeUint64(writer, value)
}

func (FfiConverterUint64) Lift(value C.uint64_t) uint64 {
	return uint64(value)
}

func (FfiConverterUint64) Read(reader io.Reader) uint64 {
	return readUint64(reader)
}

type FfiDestroyerUint64 struct{}

func (FfiDestroyerUint64) Destroy(_ uint64) {}

type FfiConverterBool struct{}

var FfiConverterBoolINSTANCE = FfiConverterBool{}

func (FfiConverterBool) Lower(value bool) C.int8_t {
	if value {
		return C.int8_t(1)
	}
	return C.int8_t(0)
}

func (FfiConverterBool) Write(writer io.Writer, value bool) {
	if value {
		writeInt8(writer, 1)
	} else {
		writeInt8(writer, 0)
	}
}

func (FfiConverterBool) Lift(value C.int8_t) bool {
	return value != 0
}

func (FfiConverterBool) Read(reader io.Reader) bool {
	return readInt8(reader) != 0
}

type FfiDestroyerBool struct{}

func (FfiDestroyerBool) Destroy(_ bool) {}

type FfiConverterString struct{}

var FfiConverterStringINSTANCE = FfiConverterString{}

func (FfiConverterString) Lift(rb RustBufferI) string {
	defer rb.Free()
	reader := rb.AsReader()
	b, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Errorf("reading reader: %w", err))
	}
	return string(b)
}

func (FfiConverterString) Read(reader io.Reader) string {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading string, expected %d, read %d", length, read_length))
	}
	return string(buffer)
}

func (FfiConverterString) Lower(value string) C.RustBuffer {
	return stringToRustBuffer(value)
}

func (FfiConverterString) Write(writer io.Writer, value string) {
	if len(value) > math.MaxInt32 {
		panic("String is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := io.WriteString(writer, value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing string, expected %d, written %d", len(value), write_length))
	}
}

type FfiDestroyerString struct{}

func (FfiDestroyerString) Destroy(_ string) {}

type FfiConverterBytes struct{}

var FfiConverterBytesINSTANCE = FfiConverterBytes{}

func (c FfiConverterBytes) Lower(value []byte) C.RustBuffer {
	return LowerIntoRustBuffer[[]byte](c, value)
}

func (c FfiConverterBytes) Write(writer io.Writer, value []byte) {
	if len(value) > math.MaxInt32 {
		panic("[]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := writer.Write(value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing []byte, expected %d, written %d", len(value), write_length))
	}
}

func (c FfiConverterBytes) Lift(rb RustBufferI) []byte {
	return LiftFromRustBuffer[[]byte](c, rb)
}

func (c FfiConverterBytes) Read(reader io.Reader) []byte {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading []byte, expected %d, read %d", length, read_length))
	}
	return buffer
}

type FfiDestroyerBytes struct{}

func (FfiDestroyerBytes) Destroy(_ []byte) {}

// All inputs to `app_crypto_open`.
type AppOpenInput struct {
	// The federated token for the customer's KMS. Zeroized after use.
	KmsCred FfiCredential
	// Provider-specific initialization (region for AWS, address for Vault).
	ProviderConfig FfiProviderConfig
	// The project id the container was sealed for.
	ProjectId string
	// The crypt it was sealed under.
	Crypt string
	// The context it was sealed with.
	Context *string
	// A container v1 from `app_crypto_seal`.
	Container []byte
	// The keys this application allows, from its own configuration and never from the
	// platform or a stored envelope. Compared without the key version, so rotation keeps
	// working. When set, any other key is refused before any token use or network call.
	// Empty means no pin, and an open then uses whatever key the stored envelope names, so
	// the SDK's client path must always set this and refuse to call without it.
	ExpectedKeyRefs []string
}

func (r *AppOpenInput) Destroy() {
	FfiDestroyerFfiCredential{}.Destroy(r.KmsCred)
	FfiDestroyerFfiProviderConfig{}.Destroy(r.ProviderConfig)
	FfiDestroyerString{}.Destroy(r.ProjectId)
	FfiDestroyerString{}.Destroy(r.Crypt)
	FfiDestroyerOptionalString{}.Destroy(r.Context)
	FfiDestroyerBytes{}.Destroy(r.Container)
	FfiDestroyerSequenceString{}.Destroy(r.ExpectedKeyRefs)
}

type FfiConverterAppOpenInput struct{}

var FfiConverterAppOpenInputINSTANCE = FfiConverterAppOpenInput{}

func (c FfiConverterAppOpenInput) Lift(rb RustBufferI) AppOpenInput {
	return LiftFromRustBuffer[AppOpenInput](c, rb)
}

func (c FfiConverterAppOpenInput) Read(reader io.Reader) AppOpenInput {
	return AppOpenInput{
		FfiConverterFfiCredentialINSTANCE.Read(reader),
		FfiConverterFfiProviderConfigINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterAppOpenInput) Lower(value AppOpenInput) C.RustBuffer {
	return LowerIntoRustBuffer[AppOpenInput](c, value)
}

func (c FfiConverterAppOpenInput) Write(writer io.Writer, value AppOpenInput) {
	FfiConverterFfiCredentialINSTANCE.Write(writer, value.KmsCred)
	FfiConverterFfiProviderConfigINSTANCE.Write(writer, value.ProviderConfig)
	FfiConverterStringINSTANCE.Write(writer, value.ProjectId)
	FfiConverterStringINSTANCE.Write(writer, value.Crypt)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Context)
	FfiConverterBytesINSTANCE.Write(writer, value.Container)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.ExpectedKeyRefs)
}

type FfiDestroyerAppOpenInput struct{}

func (_ FfiDestroyerAppOpenInput) Destroy(value AppOpenInput) {
	value.Destroy()
}

// Output of `app_crypto_open`.
type AppOpenOutput struct {
	// The plaintext.
	Plaintext []byte
}

func (r *AppOpenOutput) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Plaintext)
}

type FfiConverterAppOpenOutput struct{}

var FfiConverterAppOpenOutputINSTANCE = FfiConverterAppOpenOutput{}

func (c FfiConverterAppOpenOutput) Lift(rb RustBufferI) AppOpenOutput {
	return LiftFromRustBuffer[AppOpenOutput](c, rb)
}

func (c FfiConverterAppOpenOutput) Read(reader io.Reader) AppOpenOutput {
	return AppOpenOutput{
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterAppOpenOutput) Lower(value AppOpenOutput) C.RustBuffer {
	return LowerIntoRustBuffer[AppOpenOutput](c, value)
}

func (c FfiConverterAppOpenOutput) Write(writer io.Writer, value AppOpenOutput) {
	FfiConverterBytesINSTANCE.Write(writer, value.Plaintext)
}

type FfiDestroyerAppOpenOutput struct{}

func (_ FfiDestroyerAppOpenOutput) Destroy(value AppOpenOutput) {
	value.Destroy()
}

// All inputs to `app_crypto_seal` (the client path of `k9o.crypto.encrypt` on a
// cloud BYOK/HYOK project). One item; the SDK batches.
type AppSealInput struct {
	// The customer KMS provider. Must match a compiled-in provider feature.
	Provider KmsProvider
	// Byok or Hyok (names the container's provider tag).
	Posture FfiPosture
	// The federated token for the customer's KMS, scoped to this key. Zeroized after use.
	KmsCred FfiCredential
	// The customer KMS key version reference.
	KeyRef FfiKeyRef
	// Provider-specific initialization (region for AWS, address for Vault).
	ProviderConfig FfiProviderConfig
	// The project id, as the platform names it (`proj_…`). Bound into the AAD and the KMS
	// encryption context.
	ProjectId string
	// The crypt, `[a-z0-9-]{1,64}`. Bound into the AAD and the KMS encryption context.
	Crypt string
	// Optional caller context, UTF-8, at most 256 bytes. Bound into the AAD only, never
	// into the KMS encryption context (it may be personal data).
	Context *string
	// The plaintext.
	Plaintext []byte
	// The keys this application allows, from its own configuration and never from the
	// platform or a stored envelope. Compared without the key version, so rotation keeps
	// working. When set, any other key is refused before any token use or network call.
	// Empty means no pin, and an open then uses whatever key the stored envelope names, so
	// the SDK's client path must always set this and refuse to call without it.
	ExpectedKeyRefs []string
}

func (r *AppSealInput) Destroy() {
	FfiDestroyerKmsProvider{}.Destroy(r.Provider)
	FfiDestroyerFfiPosture{}.Destroy(r.Posture)
	FfiDestroyerFfiCredential{}.Destroy(r.KmsCred)
	FfiDestroyerFfiKeyRef{}.Destroy(r.KeyRef)
	FfiDestroyerFfiProviderConfig{}.Destroy(r.ProviderConfig)
	FfiDestroyerString{}.Destroy(r.ProjectId)
	FfiDestroyerString{}.Destroy(r.Crypt)
	FfiDestroyerOptionalString{}.Destroy(r.Context)
	FfiDestroyerBytes{}.Destroy(r.Plaintext)
	FfiDestroyerSequenceString{}.Destroy(r.ExpectedKeyRefs)
}

type FfiConverterAppSealInput struct{}

var FfiConverterAppSealInputINSTANCE = FfiConverterAppSealInput{}

func (c FfiConverterAppSealInput) Lift(rb RustBufferI) AppSealInput {
	return LiftFromRustBuffer[AppSealInput](c, rb)
}

func (c FfiConverterAppSealInput) Read(reader io.Reader) AppSealInput {
	return AppSealInput{
		FfiConverterKmsProviderINSTANCE.Read(reader),
		FfiConverterFfiPostureINSTANCE.Read(reader),
		FfiConverterFfiCredentialINSTANCE.Read(reader),
		FfiConverterFfiKeyRefINSTANCE.Read(reader),
		FfiConverterFfiProviderConfigINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterAppSealInput) Lower(value AppSealInput) C.RustBuffer {
	return LowerIntoRustBuffer[AppSealInput](c, value)
}

func (c FfiConverterAppSealInput) Write(writer io.Writer, value AppSealInput) {
	FfiConverterKmsProviderINSTANCE.Write(writer, value.Provider)
	FfiConverterFfiPostureINSTANCE.Write(writer, value.Posture)
	FfiConverterFfiCredentialINSTANCE.Write(writer, value.KmsCred)
	FfiConverterFfiKeyRefINSTANCE.Write(writer, value.KeyRef)
	FfiConverterFfiProviderConfigINSTANCE.Write(writer, value.ProviderConfig)
	FfiConverterStringINSTANCE.Write(writer, value.ProjectId)
	FfiConverterStringINSTANCE.Write(writer, value.Crypt)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Context)
	FfiConverterBytesINSTANCE.Write(writer, value.Plaintext)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.ExpectedKeyRefs)
}

type FfiDestroyerAppSealInput struct{}

func (_ FfiDestroyerAppSealInput) Destroy(value AppSealInput) {
	value.Destroy()
}

// Output of `app_crypto_seal`: a container v1, opaque to the SDK.
type AppSealOutput struct {
	// The container v1 (`0x01 ‖ len ‖ W ‖ D`), the same format the server route emits.
	Container []byte
}

func (r *AppSealOutput) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Container)
}

type FfiConverterAppSealOutput struct{}

var FfiConverterAppSealOutputINSTANCE = FfiConverterAppSealOutput{}

func (c FfiConverterAppSealOutput) Lift(rb RustBufferI) AppSealOutput {
	return LiftFromRustBuffer[AppSealOutput](c, rb)
}

func (c FfiConverterAppSealOutput) Read(reader io.Reader) AppSealOutput {
	return AppSealOutput{
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterAppSealOutput) Lower(value AppSealOutput) C.RustBuffer {
	return LowerIntoRustBuffer[AppSealOutput](c, value)
}

func (c FfiConverterAppSealOutput) Write(writer io.Writer, value AppSealOutput) {
	FfiConverterBytesINSTANCE.Write(writer, value.Container)
}

type FfiDestroyerAppSealOutput struct{}

func (_ FfiDestroyerAppSealOutput) Destroy(value AppSealOutput) {
	value.Destroy()
}

// All inputs to `zk_decrypt`.
type DecryptInput struct {
	// Customer KMS credential. Must be valid for the wrapped-DEK unwrap
	// (or oracle ciphertext decrypt). Zeroized after use.
	KmsCred FfiCredential
	// Provider-specific initialization (region for AWS, address for Vault).
	ProviderConfig FfiProviderConfig
	// The canonical (`b"KIBO"`) envelope returned by a prior `zk_encrypt` call.
	Envelope []byte
	// Strategy used at encrypt — MUST be re-supplied by the caller. Strategy
	// is not stored in the canonical envelope header (it is an AAD-bound
	// dimension in `AadContext`; the KMS verifies it matches at decrypt). The
	// caller always knows the strategy used at encrypt; it is not inferred from
	// the envelope bytes.
	Strategy FfiStrategy
	// Device attestation tier — MUST be re-supplied by the caller; never
	// stored in or recovered from the envelope. `attestation_tier` is one of
	// the three reserved AAD dimensions (`kibo:attestation_tier`) bound at
	// encrypt time; the AEAD tag verification fails if the re-supplied value
	// differs from the one used at encrypt. Symmetric with `EncryptInput`.
	// Source from deployment/project config (fixed per deployment, not per-op).
	AttestationTier FfiAttestationTier
	// Encryption context — MUST be re-supplied by the caller; never read
	// from the envelope (anti-envelope-substitution discipline).
	EncryptionContext []FfiKvPair
	// The keys this application allows, from its own configuration and never from the
	// platform or a stored envelope. Compared without the key version, so rotation keeps
	// working. When set, any other key is refused before any token use or network call.
	// Empty means no pin, and an open then uses whatever key the stored envelope names, so
	// the SDK's client path must always set this and refuse to call without it.
	ExpectedKeyRefs []string
}

func (r *DecryptInput) Destroy() {
	FfiDestroyerFfiCredential{}.Destroy(r.KmsCred)
	FfiDestroyerFfiProviderConfig{}.Destroy(r.ProviderConfig)
	FfiDestroyerBytes{}.Destroy(r.Envelope)
	FfiDestroyerFfiStrategy{}.Destroy(r.Strategy)
	FfiDestroyerFfiAttestationTier{}.Destroy(r.AttestationTier)
	FfiDestroyerSequenceFfiKvPair{}.Destroy(r.EncryptionContext)
	FfiDestroyerSequenceString{}.Destroy(r.ExpectedKeyRefs)
}

type FfiConverterDecryptInput struct{}

var FfiConverterDecryptInputINSTANCE = FfiConverterDecryptInput{}

func (c FfiConverterDecryptInput) Lift(rb RustBufferI) DecryptInput {
	return LiftFromRustBuffer[DecryptInput](c, rb)
}

func (c FfiConverterDecryptInput) Read(reader io.Reader) DecryptInput {
	return DecryptInput{
		FfiConverterFfiCredentialINSTANCE.Read(reader),
		FfiConverterFfiProviderConfigINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterFfiStrategyINSTANCE.Read(reader),
		FfiConverterFfiAttestationTierINSTANCE.Read(reader),
		FfiConverterSequenceFfiKvPairINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterDecryptInput) Lower(value DecryptInput) C.RustBuffer {
	return LowerIntoRustBuffer[DecryptInput](c, value)
}

func (c FfiConverterDecryptInput) Write(writer io.Writer, value DecryptInput) {
	FfiConverterFfiCredentialINSTANCE.Write(writer, value.KmsCred)
	FfiConverterFfiProviderConfigINSTANCE.Write(writer, value.ProviderConfig)
	FfiConverterBytesINSTANCE.Write(writer, value.Envelope)
	FfiConverterFfiStrategyINSTANCE.Write(writer, value.Strategy)
	FfiConverterFfiAttestationTierINSTANCE.Write(writer, value.AttestationTier)
	FfiConverterSequenceFfiKvPairINSTANCE.Write(writer, value.EncryptionContext)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.ExpectedKeyRefs)
}

type FfiDestroyerDecryptInput struct{}

func (_ FfiDestroyerDecryptInput) Destroy(value DecryptInput) {
	value.Destroy()
}

// Output of `zk_decrypt`.
type DecryptOutput struct {
	// Recovered plaintext bytes.
	Plaintext []byte
}

func (r *DecryptOutput) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Plaintext)
}

type FfiConverterDecryptOutput struct{}

var FfiConverterDecryptOutputINSTANCE = FfiConverterDecryptOutput{}

func (c FfiConverterDecryptOutput) Lift(rb RustBufferI) DecryptOutput {
	return LiftFromRustBuffer[DecryptOutput](c, rb)
}

func (c FfiConverterDecryptOutput) Read(reader io.Reader) DecryptOutput {
	return DecryptOutput{
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterDecryptOutput) Lower(value DecryptOutput) C.RustBuffer {
	return LowerIntoRustBuffer[DecryptOutput](c, value)
}

func (c FfiConverterDecryptOutput) Write(writer io.Writer, value DecryptOutput) {
	FfiConverterBytesINSTANCE.Write(writer, value.Plaintext)
}

type FfiDestroyerDecryptOutput struct{}

func (_ FfiDestroyerDecryptOutput) Destroy(value DecryptOutput) {
	value.Destroy()
}

// All inputs to `zk_encrypt`.
type EncryptInput struct {
	// The customer KMS provider. Must match the compiled-in provider feature.
	Provider KmsProvider
	// Custody posture (Byok or Hyok — PlatformManaged excluded).
	Posture FfiPosture
	// Explicit strategy pin. `None` → `LocalEnvelope` (posture default).
	Strategy *FfiStrategy
	// Oracle routing mode. `None` → `Brokered` (default).
	OracleMode *FfiOracleMode
	// Customer KMS credential. Customer-issued short-lived token; never
	// a platform KEK. Zeroized after use.
	KmsCred FfiCredential
	// Customer KMS key version reference.
	KeyRef FfiKeyRef
	// Provider-specific initialization (region for AWS, address for Vault).
	ProviderConfig FfiProviderConfig
	// Device attestation tier bound into the AAD.
	AttestationTier FfiAttestationTier
	// User-supplied encryption context — key/value pairs bound into the AAD
	// and re-supplied byte-for-byte at decrypt. `kibo:` prefix is reserved.
	EncryptionContext []FfiKvPair
	// Plaintext bytes to encrypt.
	Plaintext []byte
	// Project-declared crypto-posture floor. `None` = permissive default.
	PosturePolicy *FfiPosturePolicy
	// The keys this application allows, from its own configuration and never from the
	// platform or a stored envelope. Compared without the key version, so rotation keeps
	// working. When set, any other key is refused before any token use or network call.
	// Empty means no pin, and an open then uses whatever key the stored envelope names, so
	// the SDK's client path must always set this and refuse to call without it.
	ExpectedKeyRefs []string
}

func (r *EncryptInput) Destroy() {
	FfiDestroyerKmsProvider{}.Destroy(r.Provider)
	FfiDestroyerFfiPosture{}.Destroy(r.Posture)
	FfiDestroyerOptionalFfiStrategy{}.Destroy(r.Strategy)
	FfiDestroyerOptionalFfiOracleMode{}.Destroy(r.OracleMode)
	FfiDestroyerFfiCredential{}.Destroy(r.KmsCred)
	FfiDestroyerFfiKeyRef{}.Destroy(r.KeyRef)
	FfiDestroyerFfiProviderConfig{}.Destroy(r.ProviderConfig)
	FfiDestroyerFfiAttestationTier{}.Destroy(r.AttestationTier)
	FfiDestroyerSequenceFfiKvPair{}.Destroy(r.EncryptionContext)
	FfiDestroyerBytes{}.Destroy(r.Plaintext)
	FfiDestroyerOptionalFfiPosturePolicy{}.Destroy(r.PosturePolicy)
	FfiDestroyerSequenceString{}.Destroy(r.ExpectedKeyRefs)
}

type FfiConverterEncryptInput struct{}

var FfiConverterEncryptInputINSTANCE = FfiConverterEncryptInput{}

func (c FfiConverterEncryptInput) Lift(rb RustBufferI) EncryptInput {
	return LiftFromRustBuffer[EncryptInput](c, rb)
}

func (c FfiConverterEncryptInput) Read(reader io.Reader) EncryptInput {
	return EncryptInput{
		FfiConverterKmsProviderINSTANCE.Read(reader),
		FfiConverterFfiPostureINSTANCE.Read(reader),
		FfiConverterOptionalFfiStrategyINSTANCE.Read(reader),
		FfiConverterOptionalFfiOracleModeINSTANCE.Read(reader),
		FfiConverterFfiCredentialINSTANCE.Read(reader),
		FfiConverterFfiKeyRefINSTANCE.Read(reader),
		FfiConverterFfiProviderConfigINSTANCE.Read(reader),
		FfiConverterFfiAttestationTierINSTANCE.Read(reader),
		FfiConverterSequenceFfiKvPairINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterOptionalFfiPosturePolicyINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterEncryptInput) Lower(value EncryptInput) C.RustBuffer {
	return LowerIntoRustBuffer[EncryptInput](c, value)
}

func (c FfiConverterEncryptInput) Write(writer io.Writer, value EncryptInput) {
	FfiConverterKmsProviderINSTANCE.Write(writer, value.Provider)
	FfiConverterFfiPostureINSTANCE.Write(writer, value.Posture)
	FfiConverterOptionalFfiStrategyINSTANCE.Write(writer, value.Strategy)
	FfiConverterOptionalFfiOracleModeINSTANCE.Write(writer, value.OracleMode)
	FfiConverterFfiCredentialINSTANCE.Write(writer, value.KmsCred)
	FfiConverterFfiKeyRefINSTANCE.Write(writer, value.KeyRef)
	FfiConverterFfiProviderConfigINSTANCE.Write(writer, value.ProviderConfig)
	FfiConverterFfiAttestationTierINSTANCE.Write(writer, value.AttestationTier)
	FfiConverterSequenceFfiKvPairINSTANCE.Write(writer, value.EncryptionContext)
	FfiConverterBytesINSTANCE.Write(writer, value.Plaintext)
	FfiConverterOptionalFfiPosturePolicyINSTANCE.Write(writer, value.PosturePolicy)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.ExpectedKeyRefs)
}

type FfiDestroyerEncryptInput struct{}

func (_ FfiDestroyerEncryptInput) Destroy(value EncryptInput) {
	value.Destroy()
}

// Output of `zk_encrypt`.
type EncryptOutput struct {
	// ZKFE self-describing envelope. Pass opaque to `zk_decrypt`.
	Envelope []byte
}

func (r *EncryptOutput) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Envelope)
}

type FfiConverterEncryptOutput struct{}

var FfiConverterEncryptOutputINSTANCE = FfiConverterEncryptOutput{}

func (c FfiConverterEncryptOutput) Lift(rb RustBufferI) EncryptOutput {
	return LiftFromRustBuffer[EncryptOutput](c, rb)
}

func (c FfiConverterEncryptOutput) Read(reader io.Reader) EncryptOutput {
	return EncryptOutput{
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterEncryptOutput) Lower(value EncryptOutput) C.RustBuffer {
	return LowerIntoRustBuffer[EncryptOutput](c, value)
}

func (c FfiConverterEncryptOutput) Write(writer io.Writer, value EncryptOutput) {
	FfiConverterBytesINSTANCE.Write(writer, value.Envelope)
}

type FfiDestroyerEncryptOutput struct{}

func (_ FfiDestroyerEncryptOutput) Destroy(value EncryptOutput) {
	value.Destroy()
}

// Acceptable oracle-mode set for the allowed_oracle_modes policy dial.
type FfiAllowedOracleModes struct {
	Brokered bool
	Direct   bool
}

func (r *FfiAllowedOracleModes) Destroy() {
	FfiDestroyerBool{}.Destroy(r.Brokered)
	FfiDestroyerBool{}.Destroy(r.Direct)
}

type FfiConverterFfiAllowedOracleModes struct{}

var FfiConverterFfiAllowedOracleModesINSTANCE = FfiConverterFfiAllowedOracleModes{}

func (c FfiConverterFfiAllowedOracleModes) Lift(rb RustBufferI) FfiAllowedOracleModes {
	return LiftFromRustBuffer[FfiAllowedOracleModes](c, rb)
}

func (c FfiConverterFfiAllowedOracleModes) Read(reader io.Reader) FfiAllowedOracleModes {
	return FfiAllowedOracleModes{
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiAllowedOracleModes) Lower(value FfiAllowedOracleModes) C.RustBuffer {
	return LowerIntoRustBuffer[FfiAllowedOracleModes](c, value)
}

func (c FfiConverterFfiAllowedOracleModes) Write(writer io.Writer, value FfiAllowedOracleModes) {
	FfiConverterBoolINSTANCE.Write(writer, value.Brokered)
	FfiConverterBoolINSTANCE.Write(writer, value.Direct)
}

type FfiDestroyerFfiAllowedOracleModes struct{}

func (_ FfiDestroyerFfiAllowedOracleModes) Destroy(value FfiAllowedOracleModes) {
	value.Destroy()
}

// Short-lived customer KMS credential — the edge device's federated token.
// Zeroized on drop in the engine (mapped to `FederatedToken`). The `secret`
// is never logged or returned in any output or error.
type FfiCredential struct {
	// Opaque credential value (e.g., STS session token, GCP OAuth bearer,
	// Vault token). Treated as secret material; zeroized after use.
	Secret string
	// Absolute expiry as Unix seconds. The caller is responsible for refresh.
	ExpiresAtUnixSecs uint64
}

func (r *FfiCredential) Destroy() {
	FfiDestroyerString{}.Destroy(r.Secret)
	FfiDestroyerUint64{}.Destroy(r.ExpiresAtUnixSecs)
}

type FfiConverterFfiCredential struct{}

var FfiConverterFfiCredentialINSTANCE = FfiConverterFfiCredential{}

func (c FfiConverterFfiCredential) Lift(rb RustBufferI) FfiCredential {
	return LiftFromRustBuffer[FfiCredential](c, rb)
}

func (c FfiConverterFfiCredential) Read(reader io.Reader) FfiCredential {
	return FfiCredential{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiCredential) Lower(value FfiCredential) C.RustBuffer {
	return LowerIntoRustBuffer[FfiCredential](c, value)
}

func (c FfiConverterFfiCredential) Write(writer io.Writer, value FfiCredential) {
	FfiConverterStringINSTANCE.Write(writer, value.Secret)
	FfiConverterUint64INSTANCE.Write(writer, value.ExpiresAtUnixSecs)
}

type FfiDestroyerFfiCredential struct{}

func (_ FfiDestroyerFfiCredential) Destroy(value FfiCredential) {
	value.Destroy()
}

// All inputs to `acquire_kms_credential`.
type FfiFederationInput struct {
	// The provider and its configuration.
	Spec FfiFederationSpec
	// The identity token to exchange. Secret; wiped after use.
	IdentityToken string
	// The host's clock, Unix seconds. The expiry of a GCP, Azure or Vault credential is this
	// plus the provider's lifetime; AWS states its own.
	NowUnixSecs uint64
}

func (r *FfiFederationInput) Destroy() {
	FfiDestroyerFfiFederationSpec{}.Destroy(r.Spec)
	FfiDestroyerString{}.Destroy(r.IdentityToken)
	FfiDestroyerUint64{}.Destroy(r.NowUnixSecs)
}

type FfiConverterFfiFederationInput struct{}

var FfiConverterFfiFederationInputINSTANCE = FfiConverterFfiFederationInput{}

func (c FfiConverterFfiFederationInput) Lift(rb RustBufferI) FfiFederationInput {
	return LiftFromRustBuffer[FfiFederationInput](c, rb)
}

func (c FfiConverterFfiFederationInput) Read(reader io.Reader) FfiFederationInput {
	return FfiFederationInput{
		FfiConverterFfiFederationSpecINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiFederationInput) Lower(value FfiFederationInput) C.RustBuffer {
	return LowerIntoRustBuffer[FfiFederationInput](c, value)
}

func (c FfiConverterFfiFederationInput) Write(writer io.Writer, value FfiFederationInput) {
	FfiConverterFfiFederationSpecINSTANCE.Write(writer, value.Spec)
	FfiConverterStringINSTANCE.Write(writer, value.IdentityToken)
	FfiConverterUint64INSTANCE.Write(writer, value.NowUnixSecs)
}

type FfiDestroyerFfiFederationInput struct{}

func (_ FfiDestroyerFfiFederationInput) Destroy(value FfiFederationInput) {
	value.Destroy()
}

// Key version reference identifying the customer KMS key. Maps directly to
// `kibo_crypto_core::types::KeyVersionRef`; `external_ref` format is provider-specific:
// - AWS: full key ARN (`arn:aws:kms:<region>:<acct>:key/<id>`)
// - GCP: fully-qualified CryptoKey resource name
// - Azure: versioned key identifier URL (`https://<vault>.vault.azure.net/keys/<name>/<ver>`)
// - Vault: `<mount>/<key_name>:<version>` (e.g. `transit/mykey:3`)
type FfiKeyRef struct {
	// Monotone version counter across all KEK rotations and re-key transitions.
	Version uint64
	// Provider-formatted opaque identifier (see above).
	ExternalRef string
}

func (r *FfiKeyRef) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Version)
	FfiDestroyerString{}.Destroy(r.ExternalRef)
}

type FfiConverterFfiKeyRef struct{}

var FfiConverterFfiKeyRefINSTANCE = FfiConverterFfiKeyRef{}

func (c FfiConverterFfiKeyRef) Lift(rb RustBufferI) FfiKeyRef {
	return LiftFromRustBuffer[FfiKeyRef](c, rb)
}

func (c FfiConverterFfiKeyRef) Read(reader io.Reader) FfiKeyRef {
	return FfiKeyRef{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiKeyRef) Lower(value FfiKeyRef) C.RustBuffer {
	return LowerIntoRustBuffer[FfiKeyRef](c, value)
}

func (c FfiConverterFfiKeyRef) Write(writer io.Writer, value FfiKeyRef) {
	FfiConverterUint64INSTANCE.Write(writer, value.Version)
	FfiConverterStringINSTANCE.Write(writer, value.ExternalRef)
}

type FfiDestroyerFfiKeyRef struct{}

func (_ FfiDestroyerFfiKeyRef) Destroy(value FfiKeyRef) {
	value.Destroy()
}

// A single encryption-context key/value pair.
//
// `Vec<(String, String)>` cannot cross the uniffi boundary (tuples are
// unsupported); this named record is the wire-compatible replacement.
// JSON serialization: `{"key": "...", "value": "..."}` (object, not array).
type FfiKvPair struct {
	Key   string
	Value string
}

func (r *FfiKvPair) Destroy() {
	FfiDestroyerString{}.Destroy(r.Key)
	FfiDestroyerString{}.Destroy(r.Value)
}

type FfiConverterFfiKvPair struct{}

var FfiConverterFfiKvPairINSTANCE = FfiConverterFfiKvPair{}

func (c FfiConverterFfiKvPair) Lift(rb RustBufferI) FfiKvPair {
	return LiftFromRustBuffer[FfiKvPair](c, rb)
}

func (c FfiConverterFfiKvPair) Read(reader io.Reader) FfiKvPair {
	return FfiKvPair{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiKvPair) Lower(value FfiKvPair) C.RustBuffer {
	return LowerIntoRustBuffer[FfiKvPair](c, value)
}

func (c FfiConverterFfiKvPair) Write(writer io.Writer, value FfiKvPair) {
	FfiConverterStringINSTANCE.Write(writer, value.Key)
	FfiConverterStringINSTANCE.Write(writer, value.Value)
}

type FfiDestroyerFfiKvPair struct{}

func (_ FfiDestroyerFfiKvPair) Destroy(value FfiKvPair) {
	value.Destroy()
}

// Project-declared crypto-posture floor. Two sub-dials:
// `min_custody_posture` is a ladder-floor (`PlatformManaged < Byok < Hyok`);
// `allowed_oracle_modes` is an acceptable-set (`⊆ {brokered, direct}`).
//
// Default = most permissive (`PlatformManaged` floor + both oracle modes).
// `min_custody_posture: None` → `PlatformManaged` (admits every posture).
// `allowed_oracle_modes: None` → both modes allowed.
type FfiPosturePolicy struct {
	// Minimum custody posture on the sovereignty ladder. `None` = no floor.
	// Note: `None` and `Some(PlatformManaged)` are equivalent here.
	MinCustodyPosture *FfiMinPosture
	// Permitted oracle modes. `None` = both modes allowed.
	AllowedOracleModes *FfiAllowedOracleModes
}

func (r *FfiPosturePolicy) Destroy() {
	FfiDestroyerOptionalFfiMinPosture{}.Destroy(r.MinCustodyPosture)
	FfiDestroyerOptionalFfiAllowedOracleModes{}.Destroy(r.AllowedOracleModes)
}

type FfiConverterFfiPosturePolicy struct{}

var FfiConverterFfiPosturePolicyINSTANCE = FfiConverterFfiPosturePolicy{}

func (c FfiConverterFfiPosturePolicy) Lift(rb RustBufferI) FfiPosturePolicy {
	return LiftFromRustBuffer[FfiPosturePolicy](c, rb)
}

func (c FfiConverterFfiPosturePolicy) Read(reader io.Reader) FfiPosturePolicy {
	return FfiPosturePolicy{
		FfiConverterOptionalFfiMinPostureINSTANCE.Read(reader),
		FfiConverterOptionalFfiAllowedOracleModesINSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiPosturePolicy) Lower(value FfiPosturePolicy) C.RustBuffer {
	return LowerIntoRustBuffer[FfiPosturePolicy](c, value)
}

func (c FfiConverterFfiPosturePolicy) Write(writer io.Writer, value FfiPosturePolicy) {
	FfiConverterOptionalFfiMinPostureINSTANCE.Write(writer, value.MinCustodyPosture)
	FfiConverterOptionalFfiAllowedOracleModesINSTANCE.Write(writer, value.AllowedOracleModes)
}

type FfiDestroyerFfiPosturePolicy struct{}

func (_ FfiDestroyerFfiPosturePolicy) Destroy(value FfiPosturePolicy) {
	value.Destroy()
}

// Provider-specific initialization parameters. Only the field relevant to
// the compiled-in provider is consulted; others are ignored.
type FfiProviderConfig struct {
	// AWS region (e.g. `us-east-1`). Falls back to ARN-extraction from
	// `key_ref.external_ref` if `None` (ARN format only).
	AwsRegion *string
	// Vault base address (e.g. `https://vault.example.com:8200`). Required
	// when the compiled-in provider is `kms-vault`.
	VaultAddress *string
}

func (r *FfiProviderConfig) Destroy() {
	FfiDestroyerOptionalString{}.Destroy(r.AwsRegion)
	FfiDestroyerOptionalString{}.Destroy(r.VaultAddress)
}

type FfiConverterFfiProviderConfig struct{}

var FfiConverterFfiProviderConfigINSTANCE = FfiConverterFfiProviderConfig{}

func (c FfiConverterFfiProviderConfig) Lift(rb RustBufferI) FfiProviderConfig {
	return LiftFromRustBuffer[FfiProviderConfig](c, rb)
}

func (c FfiConverterFfiProviderConfig) Read(reader io.Reader) FfiProviderConfig {
	return FfiProviderConfig{
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterFfiProviderConfig) Lower(value FfiProviderConfig) C.RustBuffer {
	return LowerIntoRustBuffer[FfiProviderConfig](c, value)
}

func (c FfiConverterFfiProviderConfig) Write(writer io.Writer, value FfiProviderConfig) {
	FfiConverterOptionalStringINSTANCE.Write(writer, value.AwsRegion)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.VaultAddress)
}

type FfiDestroyerFfiProviderConfig struct{}

func (_ FfiDestroyerFfiProviderConfig) Destroy(value FfiProviderConfig) {
	value.Destroy()
}

// Device attestation tier bound into the AAD for every operation.
type FfiAttestationTier uint

const (
	FfiAttestationTierSoftware       FfiAttestationTier = 1
	FfiAttestationTierHardwareBacked FfiAttestationTier = 2
	FfiAttestationTierConfidential   FfiAttestationTier = 3
)

type FfiConverterFfiAttestationTier struct{}

var FfiConverterFfiAttestationTierINSTANCE = FfiConverterFfiAttestationTier{}

func (c FfiConverterFfiAttestationTier) Lift(rb RustBufferI) FfiAttestationTier {
	return LiftFromRustBuffer[FfiAttestationTier](c, rb)
}

func (c FfiConverterFfiAttestationTier) Lower(value FfiAttestationTier) C.RustBuffer {
	return LowerIntoRustBuffer[FfiAttestationTier](c, value)
}
func (FfiConverterFfiAttestationTier) Read(reader io.Reader) FfiAttestationTier {
	id := readInt32(reader)
	return FfiAttestationTier(id)
}

func (FfiConverterFfiAttestationTier) Write(writer io.Writer, value FfiAttestationTier) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerFfiAttestationTier struct{}

func (_ FfiDestroyerFfiAttestationTier) Destroy(value FfiAttestationTier) {
}

// Which customer provider to exchange an identity token with, and how. These values come from
// the application's own configuration, never from the platform.
type FfiFederationSpec interface {
	Destroy()
}

// AWS STS `AssumeRoleWithWebIdentity`, scoped to the one key.
type FfiFederationSpecAws struct {
	RoleArn      string
	SessionName  string
	Region       string
	KeyArn       string
	DurationSecs uint32
}

func (e FfiFederationSpecAws) Destroy() {
	FfiDestroyerString{}.Destroy(e.RoleArn)
	FfiDestroyerString{}.Destroy(e.SessionName)
	FfiDestroyerString{}.Destroy(e.Region)
	FfiDestroyerString{}.Destroy(e.KeyArn)
	FfiDestroyerUint32{}.Destroy(e.DurationSecs)
}

// GCP workload identity federation.
type FfiFederationSpecGcp struct {
	Audience string
}

func (e FfiFederationSpecGcp) Destroy() {
	FfiDestroyerString{}.Destroy(e.Audience)
}

// Microsoft Entra workload identity federation.
type FfiFederationSpecAzure struct {
	TenantId string
	ClientId string
	Scope    string
}

func (e FfiFederationSpecAzure) Destroy() {
	FfiDestroyerString{}.Destroy(e.TenantId)
	FfiDestroyerString{}.Destroy(e.ClientId)
	FfiDestroyerString{}.Destroy(e.Scope)
}

// Vault JWT login.
type FfiFederationSpecVault struct {
	VaultAddr string
	Mount     string
	Role      string
}

func (e FfiFederationSpecVault) Destroy() {
	FfiDestroyerString{}.Destroy(e.VaultAddr)
	FfiDestroyerString{}.Destroy(e.Mount)
	FfiDestroyerString{}.Destroy(e.Role)
}

type FfiConverterFfiFederationSpec struct{}

var FfiConverterFfiFederationSpecINSTANCE = FfiConverterFfiFederationSpec{}

func (c FfiConverterFfiFederationSpec) Lift(rb RustBufferI) FfiFederationSpec {
	return LiftFromRustBuffer[FfiFederationSpec](c, rb)
}

func (c FfiConverterFfiFederationSpec) Lower(value FfiFederationSpec) C.RustBuffer {
	return LowerIntoRustBuffer[FfiFederationSpec](c, value)
}
func (FfiConverterFfiFederationSpec) Read(reader io.Reader) FfiFederationSpec {
	id := readInt32(reader)
	switch id {
	case 1:
		return FfiFederationSpecAws{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
		}
	case 2:
		return FfiFederationSpecGcp{
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 3:
		return FfiFederationSpecAzure{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 4:
		return FfiFederationSpecVault{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterFfiFederationSpec.Read()", id))
	}
}

func (FfiConverterFfiFederationSpec) Write(writer io.Writer, value FfiFederationSpec) {
	switch variant_value := value.(type) {
	case FfiFederationSpecAws:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variant_value.RoleArn)
		FfiConverterStringINSTANCE.Write(writer, variant_value.SessionName)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Region)
		FfiConverterStringINSTANCE.Write(writer, variant_value.KeyArn)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.DurationSecs)
	case FfiFederationSpecGcp:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Audience)
	case FfiFederationSpecAzure:
		writeInt32(writer, 3)
		FfiConverterStringINSTANCE.Write(writer, variant_value.TenantId)
		FfiConverterStringINSTANCE.Write(writer, variant_value.ClientId)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Scope)
	case FfiFederationSpecVault:
		writeInt32(writer, 4)
		FfiConverterStringINSTANCE.Write(writer, variant_value.VaultAddr)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Mount)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Role)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterFfiFederationSpec.Write", value))
	}
}

type FfiDestroyerFfiFederationSpec struct{}

func (_ FfiDestroyerFfiFederationSpec) Destroy(value FfiFederationSpec) {
	value.Destroy()
}

// The min-custody-posture dial accepts `PlatformManaged` here (unlike
// `EncryptInput.posture` which only admits Byok/Hyok). A floor of
// `PlatformManaged` means "no floor — admit every posture including PM", which
// is the default / most permissive setting.
type FfiMinPosture uint

const (
	FfiMinPosturePlatformManaged FfiMinPosture = 1
	FfiMinPostureByok            FfiMinPosture = 2
	FfiMinPostureHyok            FfiMinPosture = 3
)

type FfiConverterFfiMinPosture struct{}

var FfiConverterFfiMinPostureINSTANCE = FfiConverterFfiMinPosture{}

func (c FfiConverterFfiMinPosture) Lift(rb RustBufferI) FfiMinPosture {
	return LiftFromRustBuffer[FfiMinPosture](c, rb)
}

func (c FfiConverterFfiMinPosture) Lower(value FfiMinPosture) C.RustBuffer {
	return LowerIntoRustBuffer[FfiMinPosture](c, value)
}
func (FfiConverterFfiMinPosture) Read(reader io.Reader) FfiMinPosture {
	id := readInt32(reader)
	return FfiMinPosture(id)
}

func (FfiConverterFfiMinPosture) Write(writer io.Writer, value FfiMinPosture) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerFfiMinPosture struct{}

func (_ FfiDestroyerFfiMinPosture) Destroy(value FfiMinPosture) {
}

// Oracle routing mode. `None` defaults to `Brokered`.
type FfiOracleMode uint

const (
	FfiOracleModeBrokered FfiOracleMode = 1
	FfiOracleModeDirect   FfiOracleMode = 2
)

type FfiConverterFfiOracleMode struct{}

var FfiConverterFfiOracleModeINSTANCE = FfiConverterFfiOracleMode{}

func (c FfiConverterFfiOracleMode) Lift(rb RustBufferI) FfiOracleMode {
	return LiftFromRustBuffer[FfiOracleMode](c, rb)
}

func (c FfiConverterFfiOracleMode) Lower(value FfiOracleMode) C.RustBuffer {
	return LowerIntoRustBuffer[FfiOracleMode](c, value)
}
func (FfiConverterFfiOracleMode) Read(reader io.Reader) FfiOracleMode {
	id := readInt32(reader)
	return FfiOracleMode(id)
}

func (FfiConverterFfiOracleMode) Write(writer io.Writer, value FfiOracleMode) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerFfiOracleMode struct{}

func (_ FfiDestroyerFfiOracleMode) Destroy(value FfiOracleMode) {
}

// Custody posture for a `zk_encrypt` call. `PlatformManaged` is deliberately
// absent — `kibo-kms-rest` is the KEK-free customer-KMS layer; a PM call has
// no customer KMS to talk to and is structurally impossible here.
type FfiPosture uint

const (
	FfiPostureByok FfiPosture = 1
	FfiPostureHyok FfiPosture = 2
)

type FfiConverterFfiPosture struct{}

var FfiConverterFfiPostureINSTANCE = FfiConverterFfiPosture{}

func (c FfiConverterFfiPosture) Lift(rb RustBufferI) FfiPosture {
	return LiftFromRustBuffer[FfiPosture](c, rb)
}

func (c FfiConverterFfiPosture) Lower(value FfiPosture) C.RustBuffer {
	return LowerIntoRustBuffer[FfiPosture](c, value)
}
func (FfiConverterFfiPosture) Read(reader io.Reader) FfiPosture {
	id := readInt32(reader)
	return FfiPosture(id)
}

func (FfiConverterFfiPosture) Write(writer io.Writer, value FfiPosture) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerFfiPosture struct{}

func (_ FfiDestroyerFfiPosture) Destroy(value FfiPosture) {
}

// Explicit strategy pin. `None` selects the posture default (`LocalEnvelope`).
type FfiStrategy uint

const (
	FfiStrategyLocalEnvelope     FfiStrategy = 1
	FfiStrategyCustomerKmsOracle FfiStrategy = 2
)

type FfiConverterFfiStrategy struct{}

var FfiConverterFfiStrategyINSTANCE = FfiConverterFfiStrategy{}

func (c FfiConverterFfiStrategy) Lift(rb RustBufferI) FfiStrategy {
	return LiftFromRustBuffer[FfiStrategy](c, rb)
}

func (c FfiConverterFfiStrategy) Lower(value FfiStrategy) C.RustBuffer {
	return LowerIntoRustBuffer[FfiStrategy](c, value)
}
func (FfiConverterFfiStrategy) Read(reader io.Reader) FfiStrategy {
	id := readInt32(reader)
	return FfiStrategy(id)
}

func (FfiConverterFfiStrategy) Write(writer io.Writer, value FfiStrategy) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerFfiStrategy struct{}

func (_ FfiDestroyerFfiStrategy) Destroy(value FfiStrategy) {
}

// The customer KMS provider for this call. The engine dispatches on it at call
// time among the providers compiled into the build (composable `kms-*` Cargo
// features); a call for a provider whose feature is not compiled in returns
// `K9O_CRYPTO_STRATEGY_UNAVAILABLE`.
type KmsProvider uint

const (
	KmsProviderAwsKms         KmsProvider = 1
	KmsProviderGcpKms         KmsProvider = 2
	KmsProviderAzureKv        KmsProvider = 3
	KmsProviderHashicorpVault KmsProvider = 4
)

type FfiConverterKmsProvider struct{}

var FfiConverterKmsProviderINSTANCE = FfiConverterKmsProvider{}

func (c FfiConverterKmsProvider) Lift(rb RustBufferI) KmsProvider {
	return LiftFromRustBuffer[KmsProvider](c, rb)
}

func (c FfiConverterKmsProvider) Lower(value KmsProvider) C.RustBuffer {
	return LowerIntoRustBuffer[KmsProvider](c, value)
}
func (FfiConverterKmsProvider) Read(reader io.Reader) KmsProvider {
	id := readInt32(reader)
	return KmsProvider(id)
}

func (FfiConverterKmsProvider) Write(writer io.Writer, value KmsProvider) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerKmsProvider struct{}

func (_ FfiDestroyerKmsProvider) Destroy(value KmsProvider) {
}

// uniffi-facing error type for `zk_encrypt` / `zk_decrypt` native exports.
//
// `ZkFfiError` is a struct and cannot directly implement `uniffi::Error`
// (which requires an enum). This enum flattens the struct into three
// variants — platform error, upstream KMS error, and posture floor
// violation — that uniffi-generated Swift/Kotlin/Python code can pattern-match.
// Converted from `ZkFfiError` at the `ffi_uniffi` boundary.
type ZkFfiUniffiError struct {
	err error
}

// Convience method to turn *ZkFfiUniffiError into error
// Avoiding treating nil pointer as non nil error interface
func (err *ZkFfiUniffiError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err ZkFfiUniffiError) Error() string {
	return fmt.Sprintf("ZkFfiUniffiError: %s", err.err.Error())
}

func (err ZkFfiUniffiError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrZkFfiUniffiErrorPlatform = fmt.Errorf("ZkFfiUniffiErrorPlatform")
var ErrZkFfiUniffiErrorUpstreamKms = fmt.Errorf("ZkFfiUniffiErrorUpstreamKms")
var ErrZkFfiUniffiErrorPostureViolation = fmt.Errorf("ZkFfiUniffiErrorPostureViolation")

// Variant structs
// A platform error with a `K9O_*` wire code.
type ZkFfiUniffiErrorPlatform struct {
	Code   string
	Detail string
}

// A platform error with a `K9O_*` wire code.
func NewZkFfiUniffiErrorPlatform(
	code string,
	detail string,
) *ZkFfiUniffiError {
	return &ZkFfiUniffiError{err: &ZkFfiUniffiErrorPlatform{
		Code:   code,
		Detail: detail}}
}

func (e ZkFfiUniffiErrorPlatform) destroy() {
	FfiDestroyerString{}.Destroy(e.Code)
	FfiDestroyerString{}.Destroy(e.Detail)
}

func (err ZkFfiUniffiErrorPlatform) Error() string {
	return fmt.Sprint("Platform",
		": ",

		"Code=",
		err.Code,
		", ",
		"Detail=",
		err.Detail,
	)
}

func (self ZkFfiUniffiErrorPlatform) Is(target error) bool {
	return target == ErrZkFfiUniffiErrorPlatform
}

// The customer's KMS returned an error. No platform code is assigned
// (upstream vendor failures are not `K9O_*` codes).
type ZkFfiUniffiErrorUpstreamKms struct {
	Detail string
}

// The customer's KMS returned an error. No platform code is assigned
// (upstream vendor failures are not `K9O_*` codes).
func NewZkFfiUniffiErrorUpstreamKms(
	detail string,
) *ZkFfiUniffiError {
	return &ZkFfiUniffiError{err: &ZkFfiUniffiErrorUpstreamKms{
		Detail: detail}}
}

func (e ZkFfiUniffiErrorUpstreamKms) destroy() {
	FfiDestroyerString{}.Destroy(e.Detail)
}

func (err ZkFfiUniffiErrorUpstreamKms) Error() string {
	return fmt.Sprint("UpstreamKms",
		": ",

		"Detail=",
		err.Detail,
	)
}

func (self ZkFfiUniffiErrorUpstreamKms) Is(target error) bool {
	return target == ErrZkFfiUniffiErrorUpstreamKms
}

// The requested posture or oracle mode is below the project-declared
// policy floor (`K9O_CRYPTO_POSTURE_FLOOR_VIOLATION`).
type ZkFfiUniffiErrorPostureViolation struct {
	Code      string
	Detail    string
	Dimension string
	Requested string
	Declared  string
}

// The requested posture or oracle mode is below the project-declared
// policy floor (`K9O_CRYPTO_POSTURE_FLOOR_VIOLATION`).
func NewZkFfiUniffiErrorPostureViolation(
	code string,
	detail string,
	dimension string,
	requested string,
	declared string,
) *ZkFfiUniffiError {
	return &ZkFfiUniffiError{err: &ZkFfiUniffiErrorPostureViolation{
		Code:      code,
		Detail:    detail,
		Dimension: dimension,
		Requested: requested,
		Declared:  declared}}
}

func (e ZkFfiUniffiErrorPostureViolation) destroy() {
	FfiDestroyerString{}.Destroy(e.Code)
	FfiDestroyerString{}.Destroy(e.Detail)
	FfiDestroyerString{}.Destroy(e.Dimension)
	FfiDestroyerString{}.Destroy(e.Requested)
	FfiDestroyerString{}.Destroy(e.Declared)
}

func (err ZkFfiUniffiErrorPostureViolation) Error() string {
	return fmt.Sprint("PostureViolation",
		": ",

		"Code=",
		err.Code,
		", ",
		"Detail=",
		err.Detail,
		", ",
		"Dimension=",
		err.Dimension,
		", ",
		"Requested=",
		err.Requested,
		", ",
		"Declared=",
		err.Declared,
	)
}

func (self ZkFfiUniffiErrorPostureViolation) Is(target error) bool {
	return target == ErrZkFfiUniffiErrorPostureViolation
}

type FfiConverterZkFfiUniffiError struct{}

var FfiConverterZkFfiUniffiErrorINSTANCE = FfiConverterZkFfiUniffiError{}

func (c FfiConverterZkFfiUniffiError) Lift(eb RustBufferI) *ZkFfiUniffiError {
	return LiftFromRustBuffer[*ZkFfiUniffiError](c, eb)
}

func (c FfiConverterZkFfiUniffiError) Lower(value *ZkFfiUniffiError) C.RustBuffer {
	return LowerIntoRustBuffer[*ZkFfiUniffiError](c, value)
}

func (c FfiConverterZkFfiUniffiError) Read(reader io.Reader) *ZkFfiUniffiError {
	errorID := readUint32(reader)

	switch errorID {
	case 1:
		return &ZkFfiUniffiError{&ZkFfiUniffiErrorPlatform{
			Code:   FfiConverterStringINSTANCE.Read(reader),
			Detail: FfiConverterStringINSTANCE.Read(reader),
		}}
	case 2:
		return &ZkFfiUniffiError{&ZkFfiUniffiErrorUpstreamKms{
			Detail: FfiConverterStringINSTANCE.Read(reader),
		}}
	case 3:
		return &ZkFfiUniffiError{&ZkFfiUniffiErrorPostureViolation{
			Code:      FfiConverterStringINSTANCE.Read(reader),
			Detail:    FfiConverterStringINSTANCE.Read(reader),
			Dimension: FfiConverterStringINSTANCE.Read(reader),
			Requested: FfiConverterStringINSTANCE.Read(reader),
			Declared:  FfiConverterStringINSTANCE.Read(reader),
		}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterZkFfiUniffiError.Read()", errorID))
	}
}

func (c FfiConverterZkFfiUniffiError) Write(writer io.Writer, value *ZkFfiUniffiError) {
	switch variantValue := value.err.(type) {
	case *ZkFfiUniffiErrorPlatform:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Code)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Detail)
	case *ZkFfiUniffiErrorUpstreamKms:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Detail)
	case *ZkFfiUniffiErrorPostureViolation:
		writeInt32(writer, 3)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Code)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Detail)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Dimension)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Requested)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Declared)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterZkFfiUniffiError.Write", value))
	}
}

type FfiDestroyerZkFfiUniffiError struct{}

func (_ FfiDestroyerZkFfiUniffiError) Destroy(value *ZkFfiUniffiError) {
	switch variantValue := value.err.(type) {
	case ZkFfiUniffiErrorPlatform:
		variantValue.destroy()
	case ZkFfiUniffiErrorUpstreamKms:
		variantValue.destroy()
	case ZkFfiUniffiErrorPostureViolation:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerZkFfiUniffiError.Destroy", value))
	}
}

type FfiConverterOptionalString struct{}

var FfiConverterOptionalStringINSTANCE = FfiConverterOptionalString{}

func (c FfiConverterOptionalString) Lift(rb RustBufferI) *string {
	return LiftFromRustBuffer[*string](c, rb)
}

func (_ FfiConverterOptionalString) Read(reader io.Reader) *string {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalString) Lower(value *string) C.RustBuffer {
	return LowerIntoRustBuffer[*string](c, value)
}

func (_ FfiConverterOptionalString) Write(writer io.Writer, value *string) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalString struct{}

func (_ FfiDestroyerOptionalString) Destroy(value *string) {
	if value != nil {
		FfiDestroyerString{}.Destroy(*value)
	}
}

type FfiConverterOptionalFfiAllowedOracleModes struct{}

var FfiConverterOptionalFfiAllowedOracleModesINSTANCE = FfiConverterOptionalFfiAllowedOracleModes{}

func (c FfiConverterOptionalFfiAllowedOracleModes) Lift(rb RustBufferI) *FfiAllowedOracleModes {
	return LiftFromRustBuffer[*FfiAllowedOracleModes](c, rb)
}

func (_ FfiConverterOptionalFfiAllowedOracleModes) Read(reader io.Reader) *FfiAllowedOracleModes {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFfiAllowedOracleModesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFfiAllowedOracleModes) Lower(value *FfiAllowedOracleModes) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiAllowedOracleModes](c, value)
}

func (_ FfiConverterOptionalFfiAllowedOracleModes) Write(writer io.Writer, value *FfiAllowedOracleModes) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFfiAllowedOracleModesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFfiAllowedOracleModes struct{}

func (_ FfiDestroyerOptionalFfiAllowedOracleModes) Destroy(value *FfiAllowedOracleModes) {
	if value != nil {
		FfiDestroyerFfiAllowedOracleModes{}.Destroy(*value)
	}
}

type FfiConverterOptionalFfiPosturePolicy struct{}

var FfiConverterOptionalFfiPosturePolicyINSTANCE = FfiConverterOptionalFfiPosturePolicy{}

func (c FfiConverterOptionalFfiPosturePolicy) Lift(rb RustBufferI) *FfiPosturePolicy {
	return LiftFromRustBuffer[*FfiPosturePolicy](c, rb)
}

func (_ FfiConverterOptionalFfiPosturePolicy) Read(reader io.Reader) *FfiPosturePolicy {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFfiPosturePolicyINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFfiPosturePolicy) Lower(value *FfiPosturePolicy) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiPosturePolicy](c, value)
}

func (_ FfiConverterOptionalFfiPosturePolicy) Write(writer io.Writer, value *FfiPosturePolicy) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFfiPosturePolicyINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFfiPosturePolicy struct{}

func (_ FfiDestroyerOptionalFfiPosturePolicy) Destroy(value *FfiPosturePolicy) {
	if value != nil {
		FfiDestroyerFfiPosturePolicy{}.Destroy(*value)
	}
}

type FfiConverterOptionalFfiMinPosture struct{}

var FfiConverterOptionalFfiMinPostureINSTANCE = FfiConverterOptionalFfiMinPosture{}

func (c FfiConverterOptionalFfiMinPosture) Lift(rb RustBufferI) *FfiMinPosture {
	return LiftFromRustBuffer[*FfiMinPosture](c, rb)
}

func (_ FfiConverterOptionalFfiMinPosture) Read(reader io.Reader) *FfiMinPosture {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFfiMinPostureINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFfiMinPosture) Lower(value *FfiMinPosture) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiMinPosture](c, value)
}

func (_ FfiConverterOptionalFfiMinPosture) Write(writer io.Writer, value *FfiMinPosture) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFfiMinPostureINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFfiMinPosture struct{}

func (_ FfiDestroyerOptionalFfiMinPosture) Destroy(value *FfiMinPosture) {
	if value != nil {
		FfiDestroyerFfiMinPosture{}.Destroy(*value)
	}
}

type FfiConverterOptionalFfiOracleMode struct{}

var FfiConverterOptionalFfiOracleModeINSTANCE = FfiConverterOptionalFfiOracleMode{}

func (c FfiConverterOptionalFfiOracleMode) Lift(rb RustBufferI) *FfiOracleMode {
	return LiftFromRustBuffer[*FfiOracleMode](c, rb)
}

func (_ FfiConverterOptionalFfiOracleMode) Read(reader io.Reader) *FfiOracleMode {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFfiOracleModeINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFfiOracleMode) Lower(value *FfiOracleMode) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiOracleMode](c, value)
}

func (_ FfiConverterOptionalFfiOracleMode) Write(writer io.Writer, value *FfiOracleMode) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFfiOracleModeINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFfiOracleMode struct{}

func (_ FfiDestroyerOptionalFfiOracleMode) Destroy(value *FfiOracleMode) {
	if value != nil {
		FfiDestroyerFfiOracleMode{}.Destroy(*value)
	}
}

type FfiConverterOptionalFfiStrategy struct{}

var FfiConverterOptionalFfiStrategyINSTANCE = FfiConverterOptionalFfiStrategy{}

func (c FfiConverterOptionalFfiStrategy) Lift(rb RustBufferI) *FfiStrategy {
	return LiftFromRustBuffer[*FfiStrategy](c, rb)
}

func (_ FfiConverterOptionalFfiStrategy) Read(reader io.Reader) *FfiStrategy {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFfiStrategyINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFfiStrategy) Lower(value *FfiStrategy) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiStrategy](c, value)
}

func (_ FfiConverterOptionalFfiStrategy) Write(writer io.Writer, value *FfiStrategy) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFfiStrategyINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFfiStrategy struct{}

func (_ FfiDestroyerOptionalFfiStrategy) Destroy(value *FfiStrategy) {
	if value != nil {
		FfiDestroyerFfiStrategy{}.Destroy(*value)
	}
}

type FfiConverterSequenceString struct{}

var FfiConverterSequenceStringINSTANCE = FfiConverterSequenceString{}

func (c FfiConverterSequenceString) Lift(rb RustBufferI) []string {
	return LiftFromRustBuffer[[]string](c, rb)
}

func (c FfiConverterSequenceString) Read(reader io.Reader) []string {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]string, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterStringINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceString) Lower(value []string) C.RustBuffer {
	return LowerIntoRustBuffer[[]string](c, value)
}

func (c FfiConverterSequenceString) Write(writer io.Writer, value []string) {
	if len(value) > math.MaxInt32 {
		panic("[]string is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterStringINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceString struct{}

func (FfiDestroyerSequenceString) Destroy(sequence []string) {
	for _, value := range sequence {
		FfiDestroyerString{}.Destroy(value)
	}
}

type FfiConverterSequenceFfiKvPair struct{}

var FfiConverterSequenceFfiKvPairINSTANCE = FfiConverterSequenceFfiKvPair{}

func (c FfiConverterSequenceFfiKvPair) Lift(rb RustBufferI) []FfiKvPair {
	return LiftFromRustBuffer[[]FfiKvPair](c, rb)
}

func (c FfiConverterSequenceFfiKvPair) Read(reader io.Reader) []FfiKvPair {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]FfiKvPair, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterFfiKvPairINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceFfiKvPair) Lower(value []FfiKvPair) C.RustBuffer {
	return LowerIntoRustBuffer[[]FfiKvPair](c, value)
}

func (c FfiConverterSequenceFfiKvPair) Write(writer io.Writer, value []FfiKvPair) {
	if len(value) > math.MaxInt32 {
		panic("[]FfiKvPair is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterFfiKvPairINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceFfiKvPair struct{}

func (FfiDestroyerSequenceFfiKvPair) Destroy(sequence []FfiKvPair) {
	for _, value := range sequence {
		FfiDestroyerFfiKvPair{}.Destroy(value)
	}
}

const (
	uniffiRustFuturePollReady      int8 = 0
	uniffiRustFuturePollMaybeReady int8 = 1
)

type rustFuturePollFunc func(C.uint64_t, C.UniffiRustFutureContinuationCallback, C.uint64_t)
type rustFutureCompleteFunc[T any] func(C.uint64_t, *C.RustCallStatus) T
type rustFutureFreeFunc func(C.uint64_t)

//export kibo_kms_rest_ffi_uniffiFutureContinuationCallback
func kibo_kms_rest_ffi_uniffiFutureContinuationCallback(data C.uint64_t, pollResult C.int8_t) {
	h := cgo.Handle(uintptr(data))
	waiter := h.Value().(chan int8)
	waiter <- int8(pollResult)
}

func uniffiRustCallAsync[E any, T any, F any](
	errConverter BufReader[*E],
	completeFunc rustFutureCompleteFunc[F],
	liftFunc func(F) T,
	rustFuture C.uint64_t,
	pollFunc rustFuturePollFunc,
	freeFunc rustFutureFreeFunc,
) (T, *E) {
	defer freeFunc(rustFuture)

	pollResult := int8(-1)
	waiter := make(chan int8, 1)

	chanHandle := cgo.NewHandle(waiter)
	defer chanHandle.Delete()

	for pollResult != uniffiRustFuturePollReady {
		pollFunc(
			rustFuture,
			(C.UniffiRustFutureContinuationCallback)(C.kibo_kms_rest_ffi_uniffiFutureContinuationCallback),
			C.uint64_t(chanHandle),
		)
		pollResult = <-waiter
	}

	var goValue T
	var ffiValue F
	var err *E

	ffiValue, err = rustCallWithError(errConverter, func(status *C.RustCallStatus) F {
		return completeFunc(rustFuture, status)
	})
	if err != nil {
		return goValue, err
	}
	return liftFunc(ffiValue), nil
}

//export kibo_kms_rest_ffi_uniffiFreeGorutine
func kibo_kms_rest_ffi_uniffiFreeGorutine(data C.uint64_t) {
	handle := cgo.Handle(uintptr(data))
	defer handle.Delete()

	guard := handle.Value().(chan struct{})
	guard <- struct{}{}
}

// uniffi-exported async exchange of an identity token for the credential `kms_cred` takes.
//
// # Errors
// [`ZkFfiUniffiError`]: a field that fails its check, or the provider's refusal.
func AcquireKmsCredential(input FfiFederationInput) (FfiCredential, error) {
	res, err := uniffiRustCallAsync[ZkFfiUniffiError](
		FfiConverterZkFfiUniffiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_kibo_kms_rest_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) FfiCredential {
			return FfiConverterFfiCredentialINSTANCE.Lift(ffi)
		},
		C.uniffi_kibo_kms_rest_ffi_fn_func_acquire_kms_credential(FfiConverterFfiFederationInputINSTANCE.Lower(input)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Sync (blocking) [`acquire_kms_credential`].
//
// # Errors
// As [`acquire_kms_credential`].
func AcquireKmsCredentialBlocking(input FfiFederationInput) (FfiCredential, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[ZkFfiUniffiError](FfiConverterZkFfiUniffiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_acquire_kms_credential_blocking(FfiConverterFfiFederationInputINSTANCE.Lower(input), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue FfiCredential
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterFfiCredentialINSTANCE.Lift(_uniffiRV), nil
	}
}

// uniffi-exported async `k9o.crypto` client-path open of a container v1.
//
// # Errors
// [`ZkFfiUniffiError`]: `K9O_ENVELOPE_INTEGRITY_FAILURE` for every cannot-open, or the KMS.
func AppCryptoOpen(input AppOpenInput) (AppOpenOutput, error) {
	res, err := uniffiRustCallAsync[ZkFfiUniffiError](
		FfiConverterZkFfiUniffiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_kibo_kms_rest_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) AppOpenOutput {
			return FfiConverterAppOpenOutputINSTANCE.Lift(ffi)
		},
		C.uniffi_kibo_kms_rest_ffi_fn_func_app_crypto_open(FfiConverterAppOpenInputINSTANCE.Lower(input)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Sync (blocking) [`app_crypto_open`].
//
// # Errors
// As [`app_crypto_open`].
func AppCryptoOpenBlocking(input AppOpenInput) (AppOpenOutput, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[ZkFfiUniffiError](FfiConverterZkFfiUniffiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_app_crypto_open_blocking(FfiConverterAppOpenInputINSTANCE.Lower(input), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue AppOpenOutput
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAppOpenOutputINSTANCE.Lift(_uniffiRV), nil
	}
}

// uniffi-exported async `k9o.crypto` client-path seal: one item, sealed in-process
// under the customer's KMS into a container v1.
//
// # Errors
// [`ZkFfiUniffiError`]: a field shape, the size limit, or the customer KMS.
func AppCryptoSeal(input AppSealInput) (AppSealOutput, error) {
	res, err := uniffiRustCallAsync[ZkFfiUniffiError](
		FfiConverterZkFfiUniffiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_kibo_kms_rest_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) AppSealOutput {
			return FfiConverterAppSealOutputINSTANCE.Lift(ffi)
		},
		C.uniffi_kibo_kms_rest_ffi_fn_func_app_crypto_seal(FfiConverterAppSealInputINSTANCE.Lower(input)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Sync (blocking) [`app_crypto_seal`], for the non-async-native uniffi languages.
//
// # Errors
// As [`app_crypto_seal`].
func AppCryptoSealBlocking(input AppSealInput) (AppSealOutput, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[ZkFfiUniffiError](FfiConverterZkFfiUniffiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_app_crypto_seal_blocking(FfiConverterAppSealInputINSTANCE.Lower(input), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue AppSealOutput
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAppSealOutputINSTANCE.Lift(_uniffiRV), nil
	}
}

// What this core enforces. An SDK refuses a set `expected_key_refs` when it's not listed here.
func CoreCapabilities() []string {
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_core_capabilities(_uniffiStatus),
		}
	}))
}

// uniffi-exported async decrypt. Returns the recovered plaintext bytes.
//
// # Errors
// [`ZkFfiUniffiError`] on a posture-floor violation, an upstream customer-KMS
// failure, or a platform error.
func ZkDecrypt(input DecryptInput) (DecryptOutput, error) {
	res, err := uniffiRustCallAsync[ZkFfiUniffiError](
		FfiConverterZkFfiUniffiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_kibo_kms_rest_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) DecryptOutput {
			return FfiConverterDecryptOutputINSTANCE.Lift(ffi)
		},
		C.uniffi_kibo_kms_rest_ffi_fn_func_zk_decrypt(FfiConverterDecryptInputINSTANCE.Lower(input)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Sync (blocking) decrypt — drives the canonical async `zk_decrypt`.
//
// # Errors
// [`ZkFfiUniffiError`] on a posture-floor violation, an upstream customer-KMS
// failure, or a platform error.
func ZkDecryptBlocking(input DecryptInput) (DecryptOutput, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[ZkFfiUniffiError](FfiConverterZkFfiUniffiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_zk_decrypt_blocking(FfiConverterDecryptInputINSTANCE.Lower(input), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue DecryptOutput
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterDecryptOutputINSTANCE.Lift(_uniffiRV), nil
	}
}

// uniffi-exported async encrypt. Returns the canonical `b"KIBO"` envelope bytes.
//
// # Errors
// [`ZkFfiUniffiError`] on a posture-floor violation, an upstream customer-KMS
// failure, or a platform error.
func ZkEncrypt(input EncryptInput) (EncryptOutput, error) {
	res, err := uniffiRustCallAsync[ZkFfiUniffiError](
		FfiConverterZkFfiUniffiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_kibo_kms_rest_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) EncryptOutput {
			return FfiConverterEncryptOutputINSTANCE.Lift(ffi)
		},
		C.uniffi_kibo_kms_rest_ffi_fn_func_zk_encrypt(FfiConverterEncryptInputINSTANCE.Lower(input)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_kibo_kms_rest_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Sync (blocking) encrypt — drives the canonical async `zk_encrypt` to
// completion. For the non-async-native uniffi languages; crypto-identical.
//
// # Errors
// [`ZkFfiUniffiError`] on a posture-floor violation, an upstream customer-KMS
// failure, or a platform error.
func ZkEncryptBlocking(input EncryptInput) (EncryptOutput, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[ZkFfiUniffiError](FfiConverterZkFfiUniffiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_kibo_kms_rest_ffi_fn_func_zk_encrypt_blocking(FfiConverterEncryptInputINSTANCE.Lower(input), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue EncryptOutput
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterEncryptOutputINSTANCE.Lift(_uniffiRV), nil
	}
}
