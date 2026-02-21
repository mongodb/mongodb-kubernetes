package diffwatch

import (
	"context"
	"encoding/json"
	"io"
	"log"
)

func parseJsonObjectsFromStream(ctx context.Context, input io.Reader, name string, parsedObjectChan chan<- ParsedFileWrapper) {
	jsonDecoder := json.NewDecoder(input)
	for {
		select {
		case <-ctx.Done():
			log.Println("exiting json stream decoder routine")
			return
		default:
		}

		mapObj := map[string]interface{}{}
		var parsedFile ParsedFileWrapper
		if err := jsonDecoder.Decode(&mapObj); err != nil {
			parsedFile = ParsedFileWrapper{err: err}
		} else {
			contentBytes, err := json.MarshalIndent(mapObj, "", " ")
			if err != nil {
				panic(err)
			}
			parsedFile = ParsedFileWrapper{
				name:         name,
				content:      string(contentBytes),
				contentAsMap: mapObj,
			}
		}

		select {
		case <-ctx.Done():
			log.Println("exiting json stream decoder routine")
			return
		case parsedObjectChan <- parsedFile:
			continue
		}
	}
}

func ReadAndParseFromStream(ctx context.Context, input io.Reader, name string, parsedFileChannel chan<- ParsedFileWrapper) {
	parsedObjectChan := make(chan ParsedFileWrapper)

	// Because jsonDecoder.Decode is blocking without any means to cancel, we need to read json objects in
	// a separate goroutine to let this function to exit when the context is canceled.
	go parseJsonObjectsFromStream(ctx, input, name, parsedObjectChan)

	for {
		select {
		case parsedObject := <-parsedObjectChan:
			select {
			case <-ctx.Done():
				log.Println("exiting ReadAndParseFromStream routine 2")
			case parsedFileChannel <- parsedObject:
				continue
			}
		case <-ctx.Done():
			log.Println("exiting ReadAndParseFromStream routine")
			return
		}
	}
}
