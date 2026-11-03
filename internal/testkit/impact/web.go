package impact

import _ "embed"

//go:embed web.cjs
var webRunner []byte

type WebExecution struct {
	Files, ExecutedFiles, FailedFiles []string
	Reason                            string
	Completed                         bool
}
