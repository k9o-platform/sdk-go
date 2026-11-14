//go:build darwin && amd64

package kibo_kms_rest_ffi

// #cgo LDFLAGS: ${SRCDIR}/lib/darwin_amd64/libkibo_kms_rest_ffi.a -liconv -lSystem -lc -lm
import "C"
