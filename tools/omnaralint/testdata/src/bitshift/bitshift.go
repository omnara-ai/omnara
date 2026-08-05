package bitshift

func rejectedShifts() {
	_ = 1 << 2 // want "bit shift operators are not allowed"
	_ = 8 >> 1 // want "bit shift operators are not allowed"

	value := 1
	value <<= 1 // want "bit shift assignment operators are not allowed"
	value >>= 1 // want "bit shift assignment operators are not allowed"
}
