//go:build !unix

package testtiming

import "os"

func peakRSSBytes(*os.ProcessState) int64 { return 0 }
