// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

package license

import lg "github.com/divmora/license-go/pkg/license"

// publicKeys are Divmora's license signing keys (base64, raw 32 bytes): the
// trust root of the license portal. On rotation add the new key first, and
// keep the old one until its licenses are reissued.
var publicKeys = []string{
	"o5nIs/8K/bCGz6jRB33Ig1h0ONr37yvVHpddzNnL46U=",
}

// DefaultKeyRing returns Divmora's public keys.
func DefaultKeyRing() *lg.KeyRing {
	var ring *lg.KeyRing
	for _, b64 := range publicKeys {
		pub, err := lg.ParsePublicKeyFromBase64(b64)
		if err != nil {
			panic("license: bad built-in public key: " + err.Error())
		}
		if ring == nil {
			ring = lg.NewKeyRing(pub)
		} else if _, err := ring.AddKey(pub); err != nil {
			panic("license: " + err.Error())
		}
	}
	return ring
}

// Default returns a Manager that trusts Divmora's keys.
func Default() *Manager {
	v, err := NewValidator(DefaultKeyRing())
	if err != nil {
		panic("license: " + err.Error())
	}
	return New(v)
}
