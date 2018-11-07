package util

import "io"

func ReadAtMost(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	n, err := io.ReadFull(r, buf)
	if err == nil || err == io.ErrUnexpectedEOF || err == io.EOF {
		return buf[:n], nil
	}

	return buf[:n], err
}
