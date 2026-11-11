//go:build !linux

package updater

import "os"

func lockDiagnosticWriter(*os.File) error           { return ErrUnsupported }
func lockClosedDiagnostic(string) (*os.File, error) { return nil, ErrUnsupported }
