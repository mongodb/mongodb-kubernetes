package diffwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

type ParsedFileWrapper struct {
	name         string
	content      string
	contentAsMap map[string]interface{}
	err          error
}

func tryToParseJsonFile(filePath string) (ParsedFileWrapper, error) {
	fileAsMap := map[string]interface{}{}
	var bytes []byte
	var err error
	for i := 0; i < 5; i++ {
		bytes, err = os.ReadFile(filePath)
		if err != nil {
			time.Sleep(time.Millisecond * 10)
			continue
		}

		if err = json.Unmarshal(bytes, &fileAsMap); err != nil {
			time.Sleep(time.Millisecond * 10)
			continue
		}
		break
	}

	if err != nil {
		return ParsedFileWrapper{}, fmt.Errorf("error unmarshalling json: \n%s\nerror: %w", string(bytes), err)
	}

	return ParsedFileWrapper{name: filePath, content: string(bytes), contentAsMap: fileAsMap}, nil
}

// ReadAndParseFilePeriodically is a blocking function that parses filePath's content periodically with watchInterval and sends the result into parsedFileChannel.
func ReadAndParseFilePeriodically(ctx context.Context, filePath string, watchInterval time.Duration, parsedFileChannel chan<- ParsedFileWrapper) {
	watchTicker := time.NewTicker(watchInterval)
	defer watchTicker.Stop()

	for {
		select {
		case <-watchTicker.C:
			parsedFile, err := tryToParseJsonFile(filePath)
			if err != nil {
				parsedFileChannel <- ParsedFileWrapper{err: err}
			}
			parsedFileChannel <- parsedFile
		case <-ctx.Done():
			log.Println("exiting fileWatcher routine")
			return
		}
	}
}
