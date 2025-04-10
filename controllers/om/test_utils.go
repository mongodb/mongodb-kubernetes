package om

import (
	"fmt"
	"os"
	"path/filepath"
)

func loadBytesFromTestData(name string) []byte {
	// testdata is a special directory ignored by "go build"
	path := filepath.Join("testdata", name)
	bytes, err := os.ReadFile(path)
	if err != nil {
		fmt.Println(err)
	}
	return bytes
}
