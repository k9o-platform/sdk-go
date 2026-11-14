//go:build darwin && arm64

package kibo_kms_rest_ffi

// #cgo LDFLAGS: ${SRCDIR}/lib/darwin_arm64/libkibo_kms_rest_ffi.a -liconv -lSystem -lc -lm
import "C"
