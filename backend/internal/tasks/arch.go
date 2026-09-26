package tasks

import "runtime"

func runtimeArch() string { return runtime.GOARCH }
