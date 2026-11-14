//go:build linux && arm64

package kibo_kms_rest_ffi

// #cgo LDFLAGS: ${SRCDIR}/lib/linux_arm64/libkibo_kms_rest_ffi.a -lgcc_s -lutil -lrt -lpthread -lm -ldl -lc
import "C"
