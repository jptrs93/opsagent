package sizeu

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var binarySizeRe = regexp.MustCompile(`^([1-9][0-9]*)(Ki|Mi|Gi|Ti)$`)

// ParseBinaryBytes parses Kubernetes-style binary quantities such as 64Mi or 1Gi.
func ParseBinaryBytes(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := binarySizeRe.FindStringSubmatch(raw)
	if parts == nil {
		return 0, fmt.Errorf("must use a binary size like 64Mi or 1Gi")
	}
	count, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size count: %w", err)
	}
	shift := int64(0)
	switch parts[2] {
	case "Ki":
		shift = 10
	case "Mi":
		shift = 20
	case "Gi":
		shift = 30
	case "Ti":
		shift = 40
	default:
		return 0, fmt.Errorf("unsupported size unit %q", parts[2])
	}
	return count << shift, nil
}

// ParseBinaryKilobytes parses Kubernetes-style binary quantities and returns kibibytes.
func ParseBinaryKilobytes(raw string) (int64, error) {
	bytes, err := ParseBinaryBytes(raw)
	if err != nil {
		return 0, err
	}
	return bytes / 1024, nil
}
