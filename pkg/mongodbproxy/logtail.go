package mongodbproxy

import (
	"bufio"
	"io"
	"os"
	"time"
)

// mongod writes its log to a file on the logs volume, which the agent and Ops Manager read. A container's log
// (kubectl logs) only has what its processes write to stdout, so the proxy follows the file and copies every line
// to its own stdout. The file stays as it is.

// defaultLogTailPoll is how often the file is looked at for new lines and for a replacement.
const defaultLogTailPoll = 100 * time.Millisecond

// tailLog copies the lines that are added to the file at path to w until done is closed, and then the lines that
// are still unread. It follows a replacement of the file (mongod renames the old log when it starts, and a log
// rotation does the same) by reading the rest of the old file and then the new one from its start. startInfo is the
// state of the file before the process was started: that file is read from its end, so that the lines of an
// earlier run are not copied again, and a file that is another one by now, or did not exist, is read from its start.
func tailLog(files logFiles, path string, startInfo os.FileInfo, w io.Writer, done <-chan struct{}, poll time.Duration) {
	var (
		file    *os.File
		info    os.FileInfo
		reader  *bufio.Reader
		partial []byte
	)
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	// copyLines writes every complete line that has been added; an unfinished line waits for its end.
	copyLines := func() {
		if reader == nil {
			return
		}
		for {
			chunk, err := reader.ReadBytes('\n')
			partial = append(partial, chunk...)
			if err != nil {
				return
			}
			_, _ = w.Write(partial)
			partial = nil
		}
	}
	open := func(skipTo int64) {
		f, err := files.open(path)
		if err != nil {
			return
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return
		}
		if skipTo > 0 {
			if _, err := f.Seek(skipTo, io.SeekStart); err != nil {
				_ = f.Close()
				return
			}
		}
		file, info, reader = f, st, bufio.NewReader(f)
	}
	// replaced reports whether the path names another file than the one that is open.
	replaced := func() bool {
		st, err := files.stat(path)
		return err == nil && (file == nil || !os.SameFile(info, st))
	}

	startOffset := func() int64 {
		if startInfo == nil {
			return 0
		}
		st, err := files.stat(path)
		if err != nil || !os.SameFile(startInfo, st) {
			return 0
		}
		return startInfo.Size()
	}
	open(startOffset())

	for {
		stopping := false
		select {
		case <-done:
			stopping = true
		case <-time.After(poll):
		}

		copyLines()
		if replaced() {
			if file != nil {
				_ = file.Close()
				if len(partial) > 0 {
					_, _ = w.Write(append(partial, '\n'))
					partial = nil
				}
			}
			open(0)
			copyLines()
		}
		if stopping {
			if len(partial) > 0 {
				_, _ = w.Write(append(partial, '\n'))
			}
			return
		}
	}
}
