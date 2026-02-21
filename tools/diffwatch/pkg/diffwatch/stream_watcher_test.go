package diffwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BlockingConcurrentBuffer is a test buffer wrapper that blocks Read operation
// if there is no available data to simulate os.Stdin behavior.
type BlockingConcurrentBuffer struct {
	buf    bytes.Buffer
	mutex  sync.Mutex
	cond   *sync.Cond
	closed bool
}

func (c *BlockingConcurrentBuffer) Write(p []byte) (n int, err error) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()
	n, err = c.buf.Write(p)
	fmt.Printf("Written: \n<%s>\n", string(p[:n]))
	c.cond.Signal()

	return n, err
}

func (c *BlockingConcurrentBuffer) Read(p []byte) (n int, err error) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()
	if c.buf.Len() == 0 {
		fmt.Printf("Empty buffer, waiting on read...\n")
		c.cond.Wait()
	}
	n, err = c.buf.Read(p)
	// if the buffer is not closed, we don't want to return EOF
	// os.Stdin is not returning EOF on a lack of data and EOF breaks json.Decoder
	if !c.closed && err == io.EOF {
		err = nil
	}
	fmt.Printf("Read: \n<%s>\n", string(p[:n]))
	return n, err
}

func (c *BlockingConcurrentBuffer) Close() {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()
	c.closed = true
}

func NewBlockingConcurrentBuffer() *BlockingConcurrentBuffer {
	return &BlockingConcurrentBuffer{
		cond: sync.NewCond(&sync.Mutex{}),
	}
}

func TestReadAndParseFromStream(t *testing.T) {
	ctx := context.Background()
	objects := []map[string]interface{}{
		{"a": "1"},
		{"b": "2"},
		{"a": "3"},
		{"a": "4",
			"b": map[string]interface{}{
				"c": map[string]interface{}{
					"d": []interface{}{"e", "f"},
				},
			},
		},
	}

	buf := NewBlockingConcurrentBuffer()

	// write the serialized data one by one, in chunks in the background
	go func() {
		for _, obj := range objects {
			jsonBytes, err := json.MarshalIndent(obj, "", "  ")
			require.NoError(t, err)
			// write in 2 chunks
			if len(jsonBytes) > 1 {
				_, err = buf.Write(jsonBytes[0 : len(jsonBytes)/2])
				_, err = buf.Write(jsonBytes[len(jsonBytes)/2:])
			} else {
				_, err = buf.Write(jsonBytes)
			}

			time.Sleep(time.Millisecond * 10)
			require.NoError(t, err)
		}
		buf.Close()
	}()

	parsedFileChannel := make(chan ParsedFileWrapper)
	// read from buf in the background and send parsed objects to parsedFileChannel
	go ReadAndParseFromStream(ctx, buf, "test", parsedFileChannel)

	// timeout here is only to not hang
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Millisecond*500)

	defer cancel()
	var parsedFiles []ParsedFileWrapper

forLabel:
	for {
		select {
		case parsedFile := <-parsedFileChannel:
			assert.NoError(t, parsedFile.err, "error on file #%d: %w", len(parsedFiles), parsedFile.err)
			parsedFiles = append(parsedFiles, parsedFile)
		case <-timeoutCtx.Done():
			log.Println("timeout done")
			buf.Close()
			break forLabel
		}
	}

	assert.Len(t, parsedFiles, len(objects))

	for i, parsedFile := range parsedFiles {
		assert.Equal(t, objects[i], parsedFile.contentAsMap)
	}
}
