package main

import "fmt"

func summaryIdleMinutes(configured *int64) (int64, error) {
	if configured == nil {
		return 50, nil
	}
	if *configured < 0 || *configured > 10080 {
		return 0, fmt.Errorf("main_agent.summary_idle_minutes must be between 0 and 10080")
	}
	return *configured, nil
}
