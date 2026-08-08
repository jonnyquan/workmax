package detectors

import "os"

// readFileBytes is the shared file read helper used by detectors that
// read sidecar markdown (brand-spec, character-spec). Centralized so
// the size cap and error semantics stay identical across detectors:
// we never want a runaway 5GB "spec" to OOM the gate; <1MB is plenty
// for any authored spec file.
const maxSpecFileBytes = 1 << 20 // 1 MiB

func readFileBytes(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err == nil && stat.Size() > maxSpecFileBytes {
		// Don't trust the file: a >1MB "spec" is either generated
		// junk or an attack. Return an error so the detector
		// degrades to Skipped with a clear reason.
		return nil, &specTooLargeError{path: path, size: stat.Size()}
	}

	data := make([]byte, maxSpecFileBytes)
	n, _ := f.Read(data)
	return data[:n], nil
}

type specTooLargeError struct {
	path string
	size int64
}

func (e *specTooLargeError) Error() string {
	return "spec file " + e.path + " exceeds 1 MiB (size: " + itoa(e.size) + ")"
}

// itoa avoids importing strconv just for an error message; rare
// enough that allocating fresh on each call doesn't matter.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
