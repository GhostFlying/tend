//go:build !linux

package nativeusage

func WriterIdentity(int) string { return "" }
