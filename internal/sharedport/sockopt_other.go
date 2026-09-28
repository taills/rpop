//go:build !linux && !darwin

package sharedport

func setNotSentLowat(uintptr, int) error { return nil }
