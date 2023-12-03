package glb

import "os"

func FileMustNotExist(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		Fatalf("'%s' already exists", dir)
	} else {
		if !os.IsNotExist(err) {
			AssertNoError(err)
		}
	}
}

func FileMustExist(dir string) {
	_, err := os.Stat(dir)
	AssertNoError(err)
}

func FileExists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
}
