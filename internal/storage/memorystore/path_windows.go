package memorystore

import (
	"errors"
	"path/filepath"
	"strings"
)

func validateNativePath(value string) error {
	if _, err := filepath.Localize(value); err != nil {
		return errors.New("memory path contains a reserved Windows name or an alternate data stream")
	}
	for _, part := range strings.Split(value, "/") {
		// Keep names portable across supported Windows versions. Newer Windows
		// accepts some device names with extensions that Server 2022 rejects.
		stem, _, extension := strings.Cut(part, ".")
		if stem = strings.TrimRight(stem, " "); extension && stem != "" {
			if _, err := filepath.Localize(stem); err != nil {
				return errors.New("memory path is not a valid Windows filename (reserved names are unsupported)")
			}
		}
		if strings.ContainsAny(part, `<>"|`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("memory filenames on Windows cannot contain < > \" | or end with a dot or space")
		}
	}
	return nil
}
