package connector

import "unicode/utf8"

func utf16OffsetToByteIndex(text string, offset int) (int, bool) {
	if offset < 0 {
		return 0, false
	}

	codeUnits := 0
	for byteIndex := 0; byteIndex < len(text); {
		if codeUnits == offset {
			return byteIndex, true
		}

		r, size := utf8.DecodeRuneInString(text[byteIndex:])
		if r <= 0xFFFF {
			codeUnits++
		} else {
			codeUnits += 2
		}
		byteIndex += size

		if codeUnits > offset {
			return 0, false
		}
		if codeUnits == offset {
			return byteIndex, true
		}
	}

	return len(text), codeUnits == offset
}

func byteIndexToUTF16Offset(text string, byteIndex int) (int, bool) {
	if byteIndex < 0 || byteIndex > len(text) {
		return 0, false
	}

	codeUnits := 0
	for currentByteIndex := 0; currentByteIndex < len(text); {
		if currentByteIndex == byteIndex {
			return codeUnits, true
		}
		if currentByteIndex > byteIndex {
			return 0, false
		}

		r, size := utf8.DecodeRuneInString(text[currentByteIndex:])
		if r <= 0xFFFF {
			codeUnits++
		} else {
			codeUnits += 2
		}
		currentByteIndex += size
	}

	return codeUnits, byteIndex == len(text)
}
