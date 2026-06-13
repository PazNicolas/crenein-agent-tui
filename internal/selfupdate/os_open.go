package selfupdate

import (
	"io"
	"os"
)

func openOsFileImpl(path string) (io.ReadCloser, error) {
	return os.Open(path)
}
