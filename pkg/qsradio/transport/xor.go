// SPDX-License-Identifier: Apache-2.0

package transport

// obfuscationKey is the fixed 16-byte XOR key used by the UV-K5 protocol.
// Source: amnemonic/Quansheng_UV-K5_Firmware python-utils/libuvk5.py payload_xor().
var obfuscationKey = [16]byte{
	0x16, 0x6c, 0x14, 0xe6, 0x2e, 0x91, 0x0d, 0x40,
	0x21, 0x35, 0xd5, 0x40, 0x13, 0x03, 0xe9, 0x80,
}

// xorObfuscate applies the rolling XOR key to data in place.
// Applying it twice returns the original data (symmetric), so the same
// function is used for both obfuscation and deobfuscation.
func xorObfuscate(data []byte) {
	for i := range data {
		data[i] ^= obfuscationKey[i%len(obfuscationKey)]
	}
}
