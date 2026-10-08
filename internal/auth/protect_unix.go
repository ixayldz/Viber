//go:build !windows

package auth

import "os"

const credentialProtection = "OWNER_ONLY_FILE_AND_DIRECTORY; NOT_ENCRYPTED_AT_REST"

func protect(raw []byte) ([]byte, error)                     { return raw, nil }
func unprotect(raw []byte) ([]byte, error)                   { return raw, nil }
func replaceCredential(root *os.Root, from, to string) error { return root.Rename(from, to) }
