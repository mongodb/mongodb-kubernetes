package main

import (
	"fmt"
	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	text1 = `Lorem ipsum dolor.
Lorem ipsum dolor3asdasdasd
asdasdasd.
Lorem ipsum dolor.≤≤
Lorem ipsum2 dolor.
Lorem ipsum dolor.
`
	text2 = `Lorem ipsum dolor.
Lorem ipsum dolor.
Lorem ipsum dolor.
Lorem ipsum dolor3asdasdasd
asdasdasd.
Lorem ipsum dolor.
Lorem ipsum3 dolor.
Lorem ipsum dolor.
A
B
C
`
)

func main() {
	dmp := diffmatchpatch.New()

	diffs := dmp.DiffMain(text1, text2, false)

	//diff2 := dmp.DiffCharsToLines(diffs, strings.Split(text2, "\n"))
	fmt.Println(dmp.DiffPrettyText(diffs))
}
