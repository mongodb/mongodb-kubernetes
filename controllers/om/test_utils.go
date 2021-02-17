package om

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
)

func loadBytesFromTestData(name string) []byte {
	// testdata is a special directory ignored by "go build"
	path := filepath.Join("testdata", name)
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Println(err)
	}
	return bytes
}
