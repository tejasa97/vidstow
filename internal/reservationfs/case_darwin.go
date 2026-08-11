//go:build darwin

package reservationfs

import (
	"errors"
	"unsafe"

	"github.com/tejasa97/vidstow/internal/reservation"
	"golang.org/x/sys/unix"
)

const (
	volCapabilitiesCaseSensitive = uint32(0x00000100)
	fsoptAttrCommonExtended      = 0x00000020
)

// detectPosixCaseSensitivity reads Darwin's authoritative volume capability
// bitmap from the already-open root handle. The valid bit is required; an
// absent valid bit is not permission to guess that the volume is
// case-sensitive.
func detectPosixCaseSensitivity(fd int, _ string) (bool, error) {
	attrList := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Volattr:     unix.ATTR_VOL_CAPABILITIES,
	}

	// fgetattrlist returns a four-byte result length followed by the selected
	// vol_capabilities_attr_t (four uint32 capability words and four valid
	// words). Keep the fixed buffer bounded and parse only the format word.
	const resultBytes = 4 + 8*4
	var result [resultBytes]byte
	r1, _, errno := unix.Syscall6(
		unix.SYS_FGETATTRLIST,
		uintptr(fd),
		uintptr(unsafe.Pointer(&attrList)),
		uintptr(unsafe.Pointer(&result[0])),
		uintptr(len(result)),
		uintptr(fsoptAttrCommonExtended),
		0,
	)
	if errno != 0 {
		return false, unsupportedError("query Darwin volume case policy", errno)
	}
	_ = r1 // fgetattrlist returns zero on success; the length lives in result.
	resultLength := readLittleEndianUint32(result[:4])
	if resultLength < resultBytes {
		return false, unsupportedError("query Darwin volume case policy", errors.New("short getattrlist result"))
	}

	capabilities := readLittleEndianUint32(result[4:8])
	valid := readLittleEndianUint32(result[20:24])
	if valid&volCapabilitiesCaseSensitive == 0 {
		return false, unsupportedError("query Darwin volume case policy", errors.New("case-sensitivity capability is not valid"))
	}
	return capabilities&volCapabilitiesCaseSensitive != 0, nil
}

func readLittleEndianUint32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func posixNameComparison(caseSensitive bool) reservation.NameComparison {
	if caseSensitive {
		// APFS lookup is normalization-insensitive even on a case-sensitive
		// volume, so byte-exact reservation comparison would miss aliases.
		return ConservativeNormalizedNames{}
	}
	return ConservativeFoldedNames{}
}
