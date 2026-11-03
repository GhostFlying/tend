//go:build !linux

package impact

import (
	"context"
	"fmt"
)

func startResourceMonitor(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("process resource sampling requires Linux")
}
