package runner

import "os"

func openEvidenceFile(path string) (*os.File, error) { return os.Open(path) }
