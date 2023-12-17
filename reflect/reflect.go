package reflect

import (
	"runtime"
)

func FunctionName() string {
	counter, _, _, success := runtime.Caller(1)

	if !success {
		println("functionName: runtime.Caller: failed")
		return "unknown()"
	}

	return runtime.FuncForPC(counter).Name() + "()"
}
