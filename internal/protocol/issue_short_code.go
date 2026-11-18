package protocol

import (
	"strconv"
	"strings"
)

// IssueShortCode formats an instance-wide Issue number for human-facing reads.
func IssueShortCode(number int64) string {
	if number <= 0 {
		return ""
	}
	return "T" + strconv.FormatInt(number, 10)
}

// IssueNumberInput parses a human lookup term without interpreting it as a
// business object identity. The caller chooses whether bare numbers are valid.
func IssueNumberInput(input string, allowBare bool) (number string, prefixed bool) {
	prefixed = strings.HasPrefix(input, "T") || strings.HasPrefix(input, "t")
	if prefixed {
		input = input[1:]
	} else if !allowBare {
		return "", false
	}
	if input == "" || input[0] == '0' {
		return "", prefixed
	}
	for _, digit := range input {
		if digit < '0' || digit > '9' {
			return "", prefixed
		}
	}
	return input, prefixed
}
