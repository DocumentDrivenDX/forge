package session

import (
	"bytes"
	"io"
)

// jsonlReader wraps a byte slice as an io.Reader for json.NewDecoder.
// Each line is a separate JSON object.
func jsonlReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}
