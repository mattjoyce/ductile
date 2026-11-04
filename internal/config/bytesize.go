package config

import "github.com/mattjoyce/ashlar"

// ParseByteSize parses human-readable byte sizes like "1MB" or "1024".
//
// Ashlar (ashlar.ParseByteSize) owns the parse: KB, MB and GB suffixes in any
// case, binary multiples, surrounding whitespace ignored, the value must be
// positive and the product must fit in int64. Its errors quote what the
// operator wrote, never the uppercased, suffix-trimmed form (C-FRO-12), and
// match ashlar.ErrValidation under errors.Is.
func ParseByteSize(size string) (int64, error) {
	return ashlar.ParseByteSize(size)
}
